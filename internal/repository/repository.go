package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

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
	db           *sql.DB
	queryTimeout time.Duration
	readOnly     bool
	scheduler    *writescheduler.Scheduler
}

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
	return r.scheduler.Submit(context.Background(), writescheduler.High, fn)
}

// execLow submits fn as a low-priority write (background ingest, retention).
func (r *Repository) execLow(fn func() error) error {
	if r.scheduler == nil {
		return fn()
	}
	return r.scheduler.Submit(context.Background(), writescheduler.Low, fn)
}
