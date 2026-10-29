package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// spansTraceIndexDDL is idx_spans_trace with the columns a trace's structure
// reads. The name stays so every INDEXED BY idx_spans_trace keeps working.
const spansTraceIndexDDL = `CREATE INDEX idx_spans_trace
	ON spans(trace_id, project_id, span_id, parent_span_id)`

// spansTraceIndexCoveringUp rebuilds idx_spans_trace as a covering index for
// the structure refresh.
//
// The index held only trace_id, so the refresh read each span of a trace from
// the table for its project_id, span_id and parent_span_id: one random read
// per span. On prod an eviction slot of four large traces (up to 24,470
// spans) held the spans writer for 10.2 s (SPA-85, 2026-10-29). With those
// columns in the key the refresh reads only the index, where one trace's
// entries sit together.
//
// The drop and the create run in the migration's transaction, so no reader
// sees the name missing. The build scans spans once at writer startup: about
// 1 s of CPU for 246k rows on staging, 10-30 s estimated on prod's cold disk,
// while the Recreate rollout has the writer down and the queue buffers
// ingest. "spans migration applied" logs its duration. An index that already
// has the columns is left alone.
func spansTraceIndexCoveringUp(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	err := tx.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_spans_trace'`).Scan(&ddl)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if strings.Contains(ddl, "parent_span_id") {
		return nil
	}
	return execSpansDDL(ctx, tx, "covering trace index",
		`DROP INDEX IF EXISTS idx_spans_trace`, spansTraceIndexDDL)
}
