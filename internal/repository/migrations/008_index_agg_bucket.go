package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up008, down008)
}

func up008(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS idx_agg_bucket_only ON aggregates(bucket)`,
		`CREATE INDEX IF NOT EXISTS idx_spans_ingested_svc ON spans(ingested_at, service, status)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func down008(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_agg_bucket_only`,
		`DROP INDEX IF EXISTS idx_spans_ingested_svc`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
