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
// a syscall).
package writescheduler

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/wiebe-xyz/spanbarn/internal/observability"
)

// tracer returns the job tracer of the current global provider. It is looked
// up per job: a tracer taken once at package init stays bound to the first
// provider set in the process, so a test that installs a recorder later would
// see no job spans.
func tracer() trace.Tracer { return otel.Tracer("spanbarn/writescheduler") }

// ErrStopped is returned by Submit once the scheduler's Run has returned.
var ErrStopped = errors.New("write scheduler stopped")

// Priority controls which queue a write job enters.
type Priority int

const (
	Low  Priority = 0
	High Priority = 1
)

// NoRows is the row count of a job that does not report one.
const NoRows int64 = -1

type job struct {
	fn       func() (int64, error)
	resultCh chan result
	label    string
	// enqueuedAt is taken before the job enters its channel, so the wait to
	// start includes time spent blocked on a full channel.
	enqueuedAt time.Time
}

// result is what a job returns: the rows it wrote, or NoRows, and its error.
type result struct {
	rows int64
	err  error
}

// Scheduler serialises DB writes through a single goroutine with two priority
// levels. Create one with New(), start it with Run(ctx), and submit work with
// Submit(). The zero value is not usable.
type Scheduler struct {
	highPri chan job
	lowPri  chan job
	logger  *slog.Logger
	// stopped closes when Run has returned. A job submitted after that has
	// nobody to run it, so Submit fails with ErrStopped instead of waiting.
	stopped chan struct{}

	// slowThreshold: a completed job slower than this is logged (WARN).
	// reportThreshold: a completed job slower than this is logged at ERROR,
	// which reaches BugBarn; WARN stays in the pod log.
	// stuckThreshold: an in-flight job older than this triggers the wedge dump.
	slowThreshold   time.Duration
	reportThreshold time.Duration
	stuckThreshold  time.Duration

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
		highPri:         make(chan job, 64),
		lowPri:          make(chan job, 512),
		stopped:         make(chan struct{}),
		logger:          slog.Default(),
		slowThreshold:   2 * time.Second,
		reportThreshold: 10 * time.Second,
		stuckThreshold:  30 * time.Second,
	}
}

// InFlight returns the label of the job being executed, or "" when idle.
func (s *Scheduler) InFlight() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.curLabel
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
	_, err := s.SubmitRows(ctx, p, label, func() (int64, error) { return NoRows, fn() })
	return err
}

// SubmitRows is Submit for a job that reports how many rows it wrote. The
// count goes on the job span as write.rows and on the slow-write log as rows,
// so a slow job's time can be read per row. A job that has no count returns
// NoRows.
func (s *Scheduler) SubmitRows(ctx context.Context, p Priority, label string, fn func() (int64, error)) (int64, error) {
	select {
	case <-s.stopped:
		return NoRows, ErrStopped
	default:
	}
	j := job{fn: fn, resultCh: make(chan result, 1), label: label, enqueuedAt: time.Now()}
	ch := s.lowPri
	if p == High {
		ch = s.highPri
	}
	select {
	case ch <- j:
	case <-ctx.Done():
		return NoRows, ctx.Err()
	case <-s.stopped:
		return NoRows, ErrStopped
	}
	select {
	case res := <-j.resultCh:
		return res.rows, res.err
	case <-s.stopped:
		// Run drains the queues before it closes stopped, so a job that made
		// it in before then has its result waiting.
		select {
		case res := <-j.resultCh:
			return res.rows, res.err
		default:
			return NoRows, ErrStopped
		}
	case <-ctx.Done():
		// The job was enqueued but the caller gave up. The scheduler will still
		// run it (we cannot cancel in-flight SQLite ops), but we return now so
		// the caller is not stuck. The result is discarded.
		return NoRows, ctx.Err()
	}
}

// Run is the scheduler loop. It must run in a dedicated goroutine and is the
// only goroutine that executes DB writes. When ctx is cancelled it runs the
// jobs already queued, then returns; later submissions fail with ErrStopped.
// Run must be called once.
func (s *Scheduler) Run(ctx context.Context) {
	observability.SafeGo("write-scheduler-watchdog", nil, func() { s.watchdog(ctx) })
	defer close(s.stopped)
	defer s.drain()
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

// drain runs the jobs left in the queues, high priority first.
func (s *Scheduler) drain() {
	for {
		select {
		case j := <-s.highPri:
			s.execute(j)
			continue
		default:
		}
		select {
		case j := <-s.lowPri:
			s.execute(j)
		default:
			return
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
	_, span := tracer().Start(context.Background(), "writescheduler.job")
	span.SetAttributes(attribute.String("write.op", label))

	start := time.Now()
	wait := start.Sub(j.enqueuedAt)
	s.mu.Lock()
	s.curLabel = label
	s.curStart = start
	s.curSeq++
	s.mu.Unlock()

	rows, err := j.fn()
	dur := time.Since(start)

	s.mu.Lock()
	s.curLabel = ""
	s.mu.Unlock()

	span.SetAttributes(
		attribute.Int64("write.duration_ms", dur.Milliseconds()),
		attribute.Int64("write.wait_ms", wait.Milliseconds()),
	)
	if rows != NoRows {
		span.SetAttributes(attribute.Int64("write.rows", rows))
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()

	s.logSlowWrite(label, dur, wait, rows)
	// Info, not Warn: the wait is the serial queue draining behind a slow job
	// and the slow job itself already files a Warn.
	if wait >= s.slowThreshold {
		s.logger.Info("write scheduler: slow queue wait", "op", label, "wait_ms", wait.Milliseconds(), "duration_ms", dur.Milliseconds())
	}
	j.resultCh <- result{rows: rows, err: err}
}

// logSlowWrite logs a job that took slowThreshold or longer. Slow writes of a
// few seconds are routine on a busy disk and stay at WARN, out of BugBarn. A
// write of reportThreshold or more holds every other write and the readers'
// checkpoint for that long, so it logs at ERROR and files in BugBarn. rows is
// logged when the job reported it.
func (s *Scheduler) logSlowWrite(label string, dur, wait time.Duration, rows int64) {
	if dur < s.slowThreshold {
		return
	}
	level := slog.LevelWarn
	if dur >= s.reportThreshold {
		level = slog.LevelError
	}
	args := []any{"op", label, "duration_ms", dur.Milliseconds(), "wait_ms", wait.Milliseconds()}
	if rows != NoRows {
		args = append(args, "rows", rows)
	}
	s.logger.Log(context.Background(), level, "write scheduler: slow write", args...)
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
