package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up004, down004)
}

func up004(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE prompt_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL,
			trace_id TEXT NOT NULL,
			span_id TEXT NOT NULL,
			parent_span_id TEXT,
			service TEXT NOT NULL,
			name TEXT NOT NULL,
			gen_ai_system TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			temperature REAL,
			max_tokens INTEGER,
			prompt_body TEXT NOT NULL DEFAULT '',
			response_body TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0,
			duration_us INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'ok',
			finish_reason TEXT NOT NULL DEFAULT '',
			prompt_template TEXT NOT NULL DEFAULT '',
			prompt_hash TEXT NOT NULL DEFAULT '',
			outcome TEXT NOT NULL DEFAULT '',
			quality_score REAL,
			feature_flag_key TEXT NOT NULL DEFAULT '',
			feature_flag_variant TEXT NOT NULL DEFAULT '',
			start_time_us INTEGER NOT NULL,
			ingested_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX idx_prompt_records_project_ingested ON prompt_records(project_id, ingested_at)`,
		`CREATE INDEX idx_prompt_records_trace ON prompt_records(trace_id)`,
		`CREATE INDEX idx_prompt_records_service ON prompt_records(project_id, service, name, start_time_us)`,
		`CREATE INDEX idx_prompt_records_model ON prompt_records(project_id, model, ingested_at)`,
		`CREATE INDEX idx_prompt_records_hash ON prompt_records(project_id, prompt_hash, ingested_at)`,
	}

	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down004(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS prompt_records")
	return err
}
