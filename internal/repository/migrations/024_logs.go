package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up024, down024)
}

func up024(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE logs (
			id                      INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id              INTEGER NOT NULL,
			trace_id                TEXT,
			span_id                 TEXT,
			severity_number         INTEGER  NOT NULL DEFAULT 0,
			severity_text           TEXT     NOT NULL DEFAULT '',
			time_unix_nano          INTEGER  NOT NULL,
			observed_time_unix_nano INTEGER  NOT NULL DEFAULT 0,
			body                    TEXT     NOT NULL DEFAULT '',
			attributes              TEXT     NOT NULL DEFAULT '{}',
			ingested_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX idx_logs_project_ingested ON logs(project_id, ingested_at)`,
		`CREATE INDEX idx_logs_trace            ON logs(trace_id) WHERE trace_id IS NOT NULL`,
		`CREATE INDEX idx_logs_project_severity ON logs(project_id, severity_number, ingested_at)`,
		`CREATE TABLE pinned_traces (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER  NOT NULL,
			trace_id   TEXT     NOT NULL,
			label      TEXT     NOT NULL DEFAULT '',
			pinned_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, trace_id)
		)`,
		`CREATE INDEX idx_pinned_traces_project ON pinned_traces(project_id, pinned_at)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down024(ctx context.Context, tx *sql.Tx) error {
	for _, s := range []string{
		`DROP TABLE IF EXISTS logs`,
		`DROP TABLE IF EXISTS pinned_traces`,
	} {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
