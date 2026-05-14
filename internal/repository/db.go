package repository

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB connection to SQLite.
type DB struct {
	*sql.DB
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
func NewDB(dbPath string) (*DB, error) {
	db, err := sql.Open("sqlite", buildDSN(dbPath, false))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	return &DB{DB: db}, nil
}

// NewReadOnlyDB opens an existing SQLite database at dbPath in read-only mode.
// Safe to use concurrently with a writer process on the same file when WAL mode
// is active on that file.
func NewReadOnlyDB(dbPath string) (*DB, error) {
	db, err := sql.Open("sqlite", buildDSN(dbPath, true))
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only %s: %w", dbPath, err)
	}
	return &DB{DB: db}, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.DB.Close()
}
