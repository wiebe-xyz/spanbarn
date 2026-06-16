package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up017, down017)
}

// up017 adds a partial index covering only root spans (parent_span_id = ”).
// QueryRootSpanGroups scans spans WHERE COALESCE(parent_span_id,”) = ” AND
// project_id = ? AND ingested_at BETWEEN ? AND ?, so it needs an index that
// can range-scan on (project_id, ingested_at) while only touching root spans.
// Without this, the query falls back to idx_spans_project_ingested and scans
// every span in the time window — including millions of child spans — before
// filtering by parent_span_id.
func up017(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_spans_root_ingested
		ON spans(project_id, ingested_at)
		WHERE COALESCE(parent_span_id,'') = ''`)
	return err
}

func down017(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_spans_root_ingested`)
	return err
}
