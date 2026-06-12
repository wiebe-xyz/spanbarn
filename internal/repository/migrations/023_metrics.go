package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up023, down023)
}

func up023(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE metrics (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id           INTEGER NOT NULL,
			name                 TEXT    NOT NULL,
			description          TEXT    NOT NULL DEFAULT '',
			unit                 TEXT    NOT NULL DEFAULT '',
			type                 TEXT    NOT NULL,
			time_unix_nano       INTEGER NOT NULL,
			start_time_unix_nano INTEGER NOT NULL DEFAULT 0,
			value                REAL    NOT NULL DEFAULT 0,
			count                INTEGER NOT NULL DEFAULT 0,
			attributes           TEXT    NOT NULL DEFAULT '{}',
			extra                TEXT,
			ingested_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX idx_metrics_project_ingested ON metrics(project_id, ingested_at)`,
		`CREATE INDEX idx_metrics_project_name     ON metrics(project_id, name, time_unix_nano)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down023(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS metrics`)
	return err
}
