package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up013, down013)
}

// up013 adds a covering index for the per-project stats query on raw spans.
// (ingested_at, project_id, status) lets SQLite resolve the 24h range scan,
// group-by, and status aggregation entirely from the index.
func up013(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_spans_project_stats ON spans(ingested_at, project_id, status)`)
	return err
}

func down013(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_spans_project_stats`)
	return err
}
