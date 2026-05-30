package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up021, down021)
}

// up021 adds a partial index covering only error spans, used by the NOT EXISTS
// clause in GetBoringTraceSpans to quickly determine whether a trace has any
// error sibling without reading the full spans table.
//
// The index is tiny (error spans are typically 1-5% of all spans) so it builds
// in seconds rather than the minutes required for a full covering index.
func up021(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_spans_error_trace
		ON spans(trace_id)
		WHERE status IN ('error', 'ERROR', 'Error')`)
	return err
}

func down021(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_spans_error_trace`)
	return err
}
