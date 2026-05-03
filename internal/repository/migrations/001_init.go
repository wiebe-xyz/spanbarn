package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up001, down001)
}

func up001(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE projects (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE api_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL REFERENCES projects(id),
			name TEXT NOT NULL,
			key_hash TEXT NOT NULL,
			scope TEXT NOT NULL DEFAULT 'ingest',
			last_used_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE spans (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL,
			trace_id TEXT NOT NULL,
			span_id TEXT NOT NULL,
			parent_span_id TEXT,
			name TEXT NOT NULL,
			service TEXT NOT NULL,
			resource TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'internal',
			status TEXT NOT NULL DEFAULT 'unset',
			start_time_us INTEGER NOT NULL,
			duration_us INTEGER NOT NULL,
			attributes TEXT NOT NULL DEFAULT '{}',
			events TEXT NOT NULL DEFAULT '[]',
			ingested_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX idx_spans_project_ingested ON spans(project_id, ingested_at)`,
		`CREATE INDEX idx_spans_trace ON spans(trace_id)`,
		`CREATE INDEX idx_spans_service_name ON spans(project_id, service, name, start_time_us)`,
		`CREATE INDEX idx_spans_status ON spans(project_id, status, ingested_at)`,

		`CREATE TABLE aggregates (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL,
			service TEXT NOT NULL,
			operation TEXT NOT NULL,
			resource TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'server',
			bucket DATETIME NOT NULL,
			count INTEGER NOT NULL DEFAULT 0,
			error_count INTEGER NOT NULL DEFAULT 0,
			p50_us INTEGER NOT NULL DEFAULT 0,
			p95_us INTEGER NOT NULL DEFAULT 0,
			p99_us INTEGER NOT NULL DEFAULT 0,
			max_us INTEGER NOT NULL DEFAULT 0,
			sum_duration_us INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX idx_agg_lookup ON aggregates(project_id, service, operation, resource, kind, bucket)`,
		`CREATE INDEX idx_agg_bucket ON aggregates(project_id, bucket)`,

		`CREATE TABLE error_samples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL,
			trace_id TEXT NOT NULL,
			span_id TEXT NOT NULL,
			parent_span_id TEXT,
			name TEXT NOT NULL,
			service TEXT NOT NULL,
			resource TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'internal',
			status TEXT NOT NULL DEFAULT 'error',
			start_time_us INTEGER NOT NULL,
			duration_us INTEGER NOT NULL,
			attributes TEXT NOT NULL DEFAULT '{}',
			events TEXT NOT NULL DEFAULT '[]',
			ingested_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			sampled_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX idx_error_samples_project ON error_samples(project_id, sampled_at)`,
		`CREATE INDEX idx_error_samples_trace ON error_samples(trace_id)`,

		`CREATE TABLE alerts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL REFERENCES projects(id),
			service TEXT NOT NULL,
			operation TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL,
			threshold REAL NOT NULL,
			comparison_window INTEGER NOT NULL DEFAULT 10,
			cooldown_minutes INTEGER NOT NULL DEFAULT 30,
			webhook_url TEXT,
			email TEXT,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_triggered_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down001(ctx context.Context, tx *sql.Tx) error {
	tables := []string{"alerts", "error_samples", "aggregates", "spans", "api_keys", "users", "projects"}
	for _, t := range tables {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+t); err != nil {
			return err
		}
	}
	return nil
}
