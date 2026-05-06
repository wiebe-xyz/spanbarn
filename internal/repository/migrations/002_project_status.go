package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up002, down002)
}

func up002(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE projects ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down002(ctx context.Context, tx *sql.Tx) error {
	// SQLite doesn't support DROP COLUMN before 3.35; recreate table.
	stmts := []string{
		`CREATE TABLE projects_backup AS SELECT id, slug, name, created_at FROM projects`,
		`DROP TABLE projects`,
		`CREATE TABLE projects (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO projects SELECT id, slug, name, created_at FROM projects_backup`,
		`DROP TABLE projects_backup`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
