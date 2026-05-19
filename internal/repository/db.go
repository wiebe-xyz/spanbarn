package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/XSAM/otelsql"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB connection to SQLite.
type DB struct {
	*sql.DB
}

func sqliteSpanName(_ context.Context, _ otelsql.Method, query string) string {
	if query != "" {
		if i := strings.IndexByte(query, ' '); i > 0 {
			return "sqlite." + strings.ToLower(strings.TrimSpace(query[:i]))
		}
	}
	return "sqlite.exec"
}

var otelOpts = []otelsql.Option{
	otelsql.WithAttributes(semconv.DBSystemSqlite),
	otelsql.WithSpanNameFormatter(sqliteSpanName),
}

// Pragmas are applied via DSN query params (_pragma=...) so they take effect on
// every connection the pool opens — not just the first one. Setting them via
// db.Exec("PRAGMA …") only configures the single connection that handled the
// Exec call; later connections spawned under load inherit no pragmas, so
// busy_timeout defaults to 0 and any contention surfaces as SQLITE_BUSY.
func buildDSN(dbPath string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "foreign_keys(ON)")
	if !readOnly {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(NORMAL)")
	}
	if readOnly {
		q.Set("mode", "ro")
	}
	prefix := "file:"
	if !strings.HasPrefix(dbPath, "file:") && dbPath != ":memory:" {
		dbPath = prefix + dbPath
	}
	return dbPath + "?" + q.Encode()
}

// NewDB opens a SQLite database at dbPath with WAL mode, busy timeout, and foreign keys enabled.
// MaxOpenConns is capped at 1: SQLite allows only one writer at a time, and when two goroutines
// each hold a separate connection and try to upgrade a deferred transaction to a write lock they
// deadlock each other — SQLite returns SQLITE_BUSY immediately, before busy_timeout can help.
// Serialising through one connection means the second writer waits in Go's pool (not in SQLite).
func NewDB(dbPath string) (*DB, error) {
	db, err := otelsql.Open("sqlite", buildDSN(dbPath, false), otelOpts...)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	// Expose connection pool stats as OTel metrics (best-effort).
	_, _ = otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(semconv.DBSystemSqlite))
	return &DB{DB: db}, nil
}

// NewReadOnlyDB opens an existing SQLite database at dbPath in read-only mode.
// Safe to use concurrently with a writer process on the same file when WAL mode
// is active on that file.
func NewReadOnlyDB(dbPath string) (*DB, error) {
	db, err := otelsql.Open("sqlite", buildDSN(dbPath, true), otelOpts...)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only %s: %w", dbPath, err)
	}
	return &DB{DB: db}, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.DB.Close()
}
