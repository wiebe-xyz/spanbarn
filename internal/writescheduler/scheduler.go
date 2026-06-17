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
package writescheduler

import "context"

// Priority controls which queue a write job enters.
type Priority int

const (
	Low  Priority = 0
	High Priority = 1
)

type job struct {
	fn       func() error
	resultCh chan error
}

// Scheduler serialises DB writes through a single goroutine with two priority
// levels. Create one with New(), start it with Run(ctx), and submit work with
// Submit(). The zero value is not usable.
type Scheduler struct {
	highPri chan job
	lowPri  chan job
}

// New creates a Scheduler with buffered channels. The buffers absorb short
// bursts so callers rarely block waiting to enqueue; the scheduler goroutine
// still processes one job at a time.
func New() *Scheduler {
	return &Scheduler{
		highPri: make(chan job, 64),
		lowPri:  make(chan job, 512),
	}
}

// Submit enqueues fn at the given priority and blocks until fn returns.
// Returns ctx.Err() if the context is cancelled while waiting to enqueue or
// waiting for the result. An in-flight job cannot be cancelled.
func (s *Scheduler) Submit(ctx context.Context, p Priority, fn func() error) error {
	j := job{fn: fn, resultCh: make(chan error, 1)}
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
	for {
		// Always drain the high-priority channel before touching low-priority.
		select {
		case j := <-s.highPri:
			j.resultCh <- j.fn()
		default:
			select {
			case j := <-s.highPri:
				j.resultCh <- j.fn()
			case j := <-s.lowPri:
				j.resultCh <- j.fn()
			case <-ctx.Done():
				return
			}
		}
	}
}
