// Package writescheduler serialises all SQLite writes through a single goroutine
// and gives high-priority writes (admin CRUD) precedence over low-priority writes
// (background span ingest, aggregate flushes, retention deletions).
//
// Without priority scheduling every write — span inserts, aggregate flushes,
// metric inserts, and admin mutations — competed in the sql.DB connection pool
// (MaxOpenConns=1 → pure FIFO). Under sustained ingest load the pool was almost
// never idle, so an admin mutation like "create alert" had to queue behind all
// pending background writes. In production this caused 20+ second waits.
//
// With the scheduler, background writers still submit to the low-priority channel
// but the scheduler goroutine always drains the high-priority channel first. An
// admin write queued while a span-insert transaction is running waits at most for
// that one transaction to finish, then runs next — regardless of how many
// low-priority jobs are pending.
//
// The scheduler is also the single instrumentation point for diagnosing writer
// "wedges": because every write runs as one fn on one goroutine, it tracks the
// in-flight job (label + start time), traces each job, and — when a job runs
// longer than stuckThreshold — logs the culprit and dumps all goroutine stacks so
// we can see exactly what is holding the connection (a slow query, a checkpoint,
// Litestream, a syscall).
package writescheduler

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var tracer = otel.Tracer("spanbarn/writescheduler")

// Priority controls which queue a write job enters.
type Priority int

const (
	Low  Priority = 0
	High Priority = 1
)

type job struct {
	fn       func() error
	resultCh chan error
	label    string
}

// Scheduler serialises DB writes through a single goroutine with two priority
// levels. Create one with New(), start it with Run(ctx), and submit work with
// Submit(). The zero value is not usable.
type Scheduler struct {
	highPri chan job
	lowPri  chan job
	logger  *slog.Logger

	// slowThreshold: a completed job slower than this is logged (WARN).
	// stuckThreshold: an in-flight job older than this triggers the wedge dump.
	slowThreshold  time.Duration
	stuckThreshold time.Duration

	mu       sync.Mutex
	curLabel string
	curStart time.Time
	curSeq   uint64 // increments per job so the watchdog dumps once per stuck job
	dumpedAt uint64
}

// New creates a Scheduler with buffered channels. The buffers absorb short
// bursts so callers rarely block waiting to enqueue; the scheduler goroutine
// still processes one job at a time.
func New() *Scheduler {
	return &Scheduler{
		highPri:        make(chan job, 64),
		lowPri:         make(chan job, 512),
		logger:         slog.Default(),
		slowThreshold:  2 * time.Second,
		stuckThreshold: 30 * time.Second,
	}
}

// SetLogger sets the logger used for slow-job and wedge diagnostics.
func (s *Scheduler) SetLogger(l *slog.Logger) {
	if l != nil {
		s.logger = l
	}
}

// Submit enqueues fn at the given priority and blocks until fn returns. label
// names the operation (for tracing and wedge diagnostics). Returns ctx.Err() if
// the context is cancelled while waiting to enqueue or waiting for the result.
// An in-flight job cannot be cancelled.
func (s *Scheduler) Submit(ctx context.Context, p Priority, label string, fn func() error) error {
	j := job{fn: fn, resultCh: make(chan error, 1), label: label}
	ch := s.lowPri
	if p == High {
		ch = s.highPri
	}
	select {
	case ch <- j:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-j.resultCh:
		return err
	case <-ctx.Done():
		// The job was enqueued but the caller gave up. The scheduler will still
		// run it (we cannot cancel in-flight SQLite ops), but we return now so
		// the caller is not stuck. The result is discarded.
		return ctx.Err()
	}
}

// Run is the scheduler loop. It must run in a dedicated goroutine and is the
// only goroutine that executes DB writes. Exits when ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	go s.watchdog(ctx)
	for {
		// Always drain the high-priority channel before touching low-priority.
		select {
		case j := <-s.highPri:
			s.execute(j)
		default:
			select {
			case j := <-s.highPri:
				s.execute(j)
			case j := <-s.lowPri:
				s.execute(j)
			case <-ctx.Done():
				return
			}
		}
	}
}

// execute runs one job, recording it as the in-flight job and tracing it so the
// watchdog and SpanBarn's own telemetry can see how long each write takes.
func (s *Scheduler) execute(j job) {
	label := j.label
	if label == "" {
		label = "unknown"
	}
	_, span := tracer.Start(context.Background(), "writescheduler.job")
	span.SetAttributes(attribute.String("write.op", label))

	start := time.Now()
	s.mu.Lock()
	s.curLabel = label
	s.curStart = start
	s.curSeq++
	s.mu.Unlock()

	err := j.fn()
	dur := time.Since(start)

	s.mu.Lock()
	s.curLabel = ""
	s.mu.Unlock()

	span.SetAttributes(attribute.Int64("write.duration_ms", dur.Milliseconds()))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()

	if dur >= s.slowThreshold {
		s.logger.Warn("write scheduler: slow write", "op", label, "duration_ms", dur.Milliseconds())
	}
	j.resultCh <- err
}

// watchdog periodically checks the in-flight job. If one has been running longer
// than stuckThreshold, it logs the culprit and dumps every goroutine's stack
// once — the direct diagnostic for a writer wedge (what is holding the single
// connection). It dumps at most once per stuck job.
func (s *Scheduler) watchdog(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			label, start, seq, dumped := s.curLabel, s.curStart, s.curSeq, s.dumpedAt
			s.mu.Unlock()
			if label == "" || start.IsZero() {
				continue
			}
			age := time.Since(start)
			if age < s.stuckThreshold || seq == dumped {
				continue
			}
			buf := make([]byte, 1<<20) // 1 MiB
			n := runtime.Stack(buf, true)
			s.logger.Error("write scheduler: WEDGE — in-flight write stuck; dumping goroutines",
				"op", label,
				"stuck_seconds", int(age.Seconds()),
				"highpri_queued", len(s.highPri),
				"lowpri_queued", len(s.lowPri),
				"goroutine_dump", string(buf[:n]),
			)
			s.mu.Lock()
			s.dumpedAt = seq
			s.mu.Unlock()
		}
	}
}
