package repository

import (
	"context"
	"database/sql"
	"time"
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
}

// NewRepository creates a Repository backed by the given database connection.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db, queryTimeout: DefaultQueryTimeout}
}

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
