package repository

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB connection to SQLite.
type DB struct {
	*sql.DB
}

// NewDB opens a SQLite database at dbPath with WAL mode, busy timeout, and foreign keys enabled.
func NewDB(dbPath string) (*DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %q: %w", p, err)
		}
	}

	return &DB{DB: db}, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.DB.Close()
}
