package repository

import (
	"context"
	"database/sql"
)

// errorStatusSQL is the status test of the error-span lookups. The partial
// indexes below repeat it word for word: SQLite uses a partial index only for
// a query whose WHERE holds the index's condition.
const errorStatusSQL = `status IN ('error','ERROR','Error')`

// spansErrorTraceIndexesUp adds partial covering indexes on the error rows of
// spans and error_samples, for the lookup of a trace's failing operations in
// error trace groups.
//
// That lookup used idx_spans_trace and idx_error_samples_trace, which hold
// only trace_id, so it fetched every row of each trace to test its status.
// Rows carry the attributes and events JSON, so on prod a 500-trace chunk
// meant some 33,000 random row reads and took 24.6 s cold, and error groups
// over 24h ran past the read budget. These indexes hold the columns the
// lookup reads, status included so the planner counts them as covering, and
// the lookup names them with INDEXED BY: without table statistics the planner
// picks the trace_id index, which looks as selective.
//
// Only error rows enter them: about 0.5% of spans and 1.5% of error_samples
// on staging. A write pays for them only when it inserts or deletes an error
// row. The build scans both tables once.
func spansErrorTraceIndexesUp(ctx context.Context, tx *sql.Tx) error {
	return execSpansDDL(ctx, tx, "error trace indexes",
		`CREATE INDEX IF NOT EXISTS idx_spans_error_trace
			ON spans(trace_id, project_id, name, service, status) WHERE `+errorStatusSQL,
		`CREATE INDEX IF NOT EXISTS idx_error_samples_error_trace
			ON error_samples(trace_id, project_id, name, service, status) WHERE `+errorStatusSQL)
}
