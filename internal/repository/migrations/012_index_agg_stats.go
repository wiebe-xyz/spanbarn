package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up012, down012)
}

// up012 adds a covering index used by the per-project stats query.
// (bucket, project_id, count, error_count) lets SQLite satisfy the
// 24h range scan + group-by entirely from the index without touching
// the wide aggregate rows.
func up012(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_agg_stats ON aggregates(bucket, project_id, count, error_count)`)
	return err
}

func down012(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_agg_stats`)
	return err
}
