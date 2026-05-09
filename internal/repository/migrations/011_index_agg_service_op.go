package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up011, down011)
}

func up011(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_agg_service_op_bucket ON aggregates(service, operation, bucket)`,
		`CREATE INDEX IF NOT EXISTS idx_spans_span_id ON spans(span_id)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down011(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`DROP INDEX IF EXISTS idx_agg_service_op_bucket`,
		`DROP INDEX IF EXISTS idx_spans_span_id`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
