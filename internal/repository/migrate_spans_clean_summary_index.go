package repository

import (
	"context"
	"database/sql"
)

// spansCleanSummaryIndexUp adds a partial index on the summaries without an
// error, newest first. The trace list reads the errors first (#457) and then
// the rest across all projects, and no index led with ingested_at for the
// rows without an error: that read scanned the table and sorted it. About 99%
// of summaries have no error, so the index holds one entry per summary, as
// idx_trace_summaries_list does.
func spansCleanSummaryIndexUp(ctx context.Context, tx *sql.Tx) error {
	return execSpansDDL(ctx, tx, "clean summary index",
		`CREATE INDEX IF NOT EXISTS idx_trace_summaries_clean
			ON trace_summaries(ingested_at DESC) WHERE has_error = 0`)
}
