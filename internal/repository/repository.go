package repository

import (
	"context"
	"database/sql"
	"runtime"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// writeOpLabel names the repository method that submitted a write, captured from
// the call stack, so the write scheduler's tracing and wedge diagnostics can say
// which operation is running/stuck without threading a label through every call.
// skip=0 → writeOpLabel, 1 → execHigh/execLow, 2 → the repo method.
func writeOpLabel() string {
	pc, _, _, ok := runtime.Caller(2)
	if !ok {
		return "unknown"
	}
	f := runtime.FuncForPC(pc)
	if f == nil {
		return "unknown"
	}
	name := f.Name() // e.g. .../internal/repository.(*Repository).InsertSpansStaging
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

const DefaultQueryTimeout = 30 * time.Second

// Repository provides data access methods over a SQLite database.
// Methods are organized into domain-specific files:
//   - repo_projects.go   — project CRUD
//   - repo_users.go      — user CRUD
//   - repo_apikeys.go    — API key CRUD
//   - repo_spans.go      — span storage, queries, service stats
//   - repo_aggregates.go — pre-computed aggregate CRUD
//   - repo_alerts.go     — alert CRUD, error samples
type Repository struct {
	db               *sql.DB
	queryTimeout     time.Duration
	readOnly         bool
	scheduler        *writescheduler.Scheduler
	deleteBatchYield time.Duration
}

// retentionDeleteBatch caps the rows touched by a single batched retention
// DELETE. Bounding each statement keeps the SQLite write-lock hold short so the
// periodic WAL checkpoint and concurrent read-only queries are never starved
// during a large purge (an unbatched DELETE here previously held the lock for
// 40–140s, blocking the WAL checkpoint and timing out reads such as the prompts
// page).
const retentionDeleteBatch = 1000

// NewRepository creates a Repository backed by the given database connection.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db, queryTimeout: DefaultQueryTimeout}
}

// NewReadOnlyRepository creates a Repository that is flagged as read-only.
// Write operations will fail at the SQLite level; this flag lets callers skip
// registering write-only endpoints on pods that open read-only connections.
func NewReadOnlyRepository(db *sql.DB) *Repository {
	return &Repository{db: db, queryTimeout: DefaultQueryTimeout, readOnly: true}
}

// ReadOnly reports whether this repository was opened against a read-only DB.
func (r *Repository) ReadOnly() bool { return r.readOnly }

// SetQueryTimeout overrides the default query timeout.
func (r *Repository) SetQueryTimeout(d time.Duration) {
	r.queryTimeout = d
}

func (r *Repository) queryContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), r.queryTimeout)
}

// DB returns the underlying *sql.DB, useful for testing.
func (r *Repository) DB() *sql.DB {
	return r.db
}

// SetWriteScheduler wires in a priority write scheduler. When set, all write
// methods route through the scheduler instead of competing in the sql.DB pool.
// Call before starting any goroutines that use this repository.
func (r *Repository) SetWriteScheduler(s *writescheduler.Scheduler) {
	r.scheduler = s
}

// execHigh submits fn as a high-priority write (admin CRUD). It blocks until
// fn completes. Falls back to a direct call when no scheduler is configured
// (tests, CLI commands).
func (r *Repository) execHigh(fn func() error) error {
	if r.scheduler == nil {
		return fn()
	}
	return r.scheduler.Submit(context.Background(), writescheduler.High, writeOpLabel(), fn)
}

// execLow submits fn as a low-priority write (background ingest, retention).
func (r *Repository) execLow(fn func() error) error {
	if r.scheduler == nil {
		return fn()
	}
	return r.scheduler.Submit(context.Background(), writescheduler.Low, writeOpLabel(), fn)
}

// execLowAffecting runs a single low-priority write statement and returns the
// number of rows it affected. It captures the execLow+Exec+RowsAffected boiler-
// plate shared by the retention DeleteXOlderThan / DeleteXByY methods.
func (r *Repository) execLowAffecting(query string, args ...any) (int64, error) {
	var n int64
	err := r.execLow(func() error {
		res, e := r.db.Exec(query, args...)
		if e != nil {
			return e
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// execHighExpectingRows runs a single high-priority write (admin update/delete
// by key) and returns sql.ErrNoRows when it affected no rows, i.e. the target
// row did not exist. Shared by the admin CRUD methods.
func (r *Repository) execHighExpectingRows(query string, args ...any) error {
	return r.execHigh(func() error {
		res, err := r.db.Exec(query, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// SetDeleteBatchYield sets how long batchedDelete pauses between batches. The
// pause releases the write lock so the periodic WAL checkpoint and read-only
// queries get a turn during a large retention purge. 0 (the default) disables
// the pause; batches still release the lock between submissions, the pause just
// guarantees the checkpoint a contention-free window.
func (r *Repository) SetDeleteBatchYield(d time.Duration) { r.deleteBatchYield = d }

// batchedDelete repeatedly runs exec — a single retentionDeleteBatch-bounded
// DELETE returning the rows it affected — through the low-priority write queue
// until a batch deletes fewer than retentionDeleteBatch rows (i.e. the tail is
// reached). Between batches it releases the write lock and, when a yield is
// configured, pauses so the WAL checkpoint and readers are not starved. It
// returns the total rows deleted.
func (r *Repository) batchedDelete(ctx context.Context, exec func() (int64, error)) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var n int64
		if err := r.execLow(func() error {
			var e error
			n, e = exec()
			return e
		}); err != nil {
			return total, err
		}
		total += n
		if n < retentionDeleteBatch {
			return total, nil
		}
		if r.deleteBatchYield > 0 {
			select {
			case <-ctx.Done():
				return total, ctx.Err()
			case <-time.After(r.deleteBatchYield):
			}
		}
	}
}
