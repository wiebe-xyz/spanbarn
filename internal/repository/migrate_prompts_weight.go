package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaExec is the part of *sql.DB and *sql.Tx a schema change needs.
type schemaExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// addPromptSampleWeight adds sample_weight to prompt_records (issue #333): how
// many traces the record's trace stands for, as the ingest trace buffer decided
// when it kept the trace. It is nullable without a default, so ADD COLUMN only
// rewrites the schema, existing rows read NULL, and an older binary that does
// not name the column keeps working.
//
// A handle whose main file has no prompt_records (a logs or metrics shard, or
// main once the table is retired) is left alone, and so is a table that already
// has the column.
func addPromptSampleWeight(ctx context.Context, db schemaExec) error {
	var tables, columns int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM main.sqlite_master WHERE type = 'table' AND name = 'prompt_records'`).Scan(&tables); err != nil {
		return fmt.Errorf("prompt sample weight: %w", err)
	}
	if tables == 0 {
		return nil
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('prompt_records', 'main') WHERE name = 'sample_weight'`).Scan(&columns); err != nil {
		return fmt.Errorf("prompt sample weight: %w", err)
	}
	if columns > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE main.prompt_records ADD COLUMN sample_weight INTEGER`); err != nil {
		return fmt.Errorf("prompt sample weight: %w", err)
	}
	return nil
}
