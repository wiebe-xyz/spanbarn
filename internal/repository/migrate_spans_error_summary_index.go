package repository

import (
	"context"
	"database/sql"
)

// errorSummaryColumns are the trace_summaries columns error trace groups read
// and filter on after the key, so the indexes below cover the query.
// has_error is listed so the planner counts the partial indexes as covering.
const errorSummaryColumns = `root_name, root_service, root_duration_us, trace_id, has_error`

// spansErrorSummaryIndexesUp adds partial covering indexes on the error rows
// of trace_summaries, for the trace read of error trace groups.
//
// That read selects has_error = 1 in a window, newest first. The only index
// with ingested_at, idx_trace_summaries_list, leads with project_id, so across
// all projects the planner scanned the table and sorted in a temp B-tree: on
// prod 101,573 rows for some 1,140 error traces in 24h, 7.5 s cold. The first
// index serves the read across all projects, the second the read of one
// project. Both are ordered by ingested_at DESC and hold the columns the read
// returns, so it walks the window's error rows alone and stops at the limit.
//
// Only error traces enter them, about 1% of summaries on prod.
func spansErrorSummaryIndexesUp(ctx context.Context, tx *sql.Tx) error {
	return execSpansDDL(ctx, tx, "error summary indexes",
		`CREATE INDEX IF NOT EXISTS idx_trace_summaries_error
			ON trace_summaries(ingested_at DESC, `+errorSummaryColumns+`) WHERE has_error = 1`,
		`CREATE INDEX IF NOT EXISTS idx_trace_summaries_error_project
			ON trace_summaries(project_id, ingested_at DESC, `+errorSummaryColumns+`) WHERE has_error = 1`)
}
