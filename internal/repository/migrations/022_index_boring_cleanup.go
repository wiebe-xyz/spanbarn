package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up022, down022)
}

// up022 adds a covering index for the fast boring-span deletion query.
// The retention worker periodically runs:
//
//	DELETE FROM spans WHERE ingested_at < ? AND status NOT IN ('error') AND duration_us < ?
//
// (ingested_at, status, duration_us) lets SQLite resolve the range + predicate
// entirely from the index without touching the main table rows.
func up022(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_spans_boring_cleanup
		ON spans(ingested_at, status, duration_us)
	`)
	return err
}

func down022(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_spans_boring_cleanup`)
	return err
}
