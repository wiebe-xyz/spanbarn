package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up006, down006)
}

func up006(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS saved_queries (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL REFERENCES projects(id),
			name       TEXT NOT NULL,
			service    TEXT NOT NULL DEFAULT '',
			operation  TEXT NOT NULL DEFAULT '',
			status     TEXT NOT NULL DEFAULT '',
			min_duration_us INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_saved_queries_project ON saved_queries(project_id);
	`)
	return err
}

func down006(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS saved_queries`)
	return err
}
