package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up038, down038)
}

// up038 adds a covering index for the dashboard queries.
//
// The dashboard runs six aggregate queries over every span in the window. Each
// one walked an ingested_at index and then fetched the full row from the spans
// table for service, name, duration_us or parent_span_id. Those rows carry the
// attributes and events JSON, so on a multi-GB database file every row is a
// random page read: the first dashboard load after a pod restart took 20s and
// the page sat on "pending" while the reader contended for the disk.
//
// This index holds every column those queries read, with ingested_at first so
// the all-projects view (no project_id) still gets a range scan. The planner
// then answers them from the index alone. idx_spans_http_status leads with
// project_id and cannot serve the all-projects view; it stays because other
// queries pin it by name.
//
// Spans are short-lived (retention keeps the table to tens of thousands of
// rows), so the build is well under a second and needs no backfill.
//
// The status-code chart cannot be covered: SQLite does not treat the virtual
// http_status column as readable from an index, so it always visits the row. A
// partial index on http_status IS NOT NULL keeps those visits to the spans that
// carry a status code instead of every span in the window.
func up038(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_spans_dashboard
			ON spans(ingested_at, project_id, service, name, status, parent_span_id, duration_us)`,
		`CREATE INDEX IF NOT EXISTS idx_spans_dashboard_http
			ON spans(ingested_at, project_id, parent_span_id, http_status) WHERE http_status IS NOT NULL`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down038(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_spans_dashboard_http`,
		`DROP INDEX IF EXISTS idx_spans_dashboard`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
