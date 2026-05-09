package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up010, down010)
}

func up010(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_spans_service_ingested ON spans(service, ingested_at)`)
	return err
}

func down010(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_spans_service_ingested`)
	return err
}
