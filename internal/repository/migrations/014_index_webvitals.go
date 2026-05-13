package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up014, down014)
}

// up014 adds a partial index over webvital spans only. QueryWebVitals does
// `WHERE name LIKE 'webvital.%' AND ingested_at >= ?`; without this index
// SQLite has to choose between the per-name index (slow because there are
// many webvital.* names) and a full table scan (slow because there are many
// non-webvital spans). A partial index limited to webvital rows ordered by
// ingested_at is small and ideal for the query.
func up014(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_spans_webvitals
		 ON spans(ingested_at)
		 WHERE name LIKE 'webvital.%'`)
	return err
}

func down014(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_spans_webvitals`)
	return err
}
