package repository

import (
	"context"
	"database/sql"
	"time"
)

// traceStructureBatch is how many summaries one backfill transaction refreshes.
// Each refresh is a handful of idx_spans_trace seeks, so a batch holds the write
// connection for milliseconds, not seconds.
const traceStructureBatch = 200

// TraceKey identifies one trace within a project.
type TraceKey struct {
	ProjectID int64
	TraceID   string
}

// Structural recompute of one summary from the spans table.
//
// A root is a span with no parent. An orphan is a span whose parent_span_id is
// set but matches no span_id in the same trace. Both lookups seek
// idx_spans_trace, so the cost depends on the size of one trace, not the table.
//
// Every spans reference is pinned with INDEXED BY idx_spans_trace. Without it
// the planner (no ANALYZE stats) picked idx_spans_http_status (project_id=?),
// which walks every span of the project once per trace and made a 500-span
// batch cost seconds. The pin fails loudly if the index is ever dropped.
// span_count is recomputed from the same rows, which keeps it equal to what the
// trace detail shows after spans have been evicted.
//
// The WHERE EXISTS guard leaves a summary alone when none of its spans remain
// (an error trace whose spans were evicted keeps listing until the error
// cutoff). settleTraceStructureSQL then records what the summary itself knows.
const (
	refreshTraceStructureSQL = `UPDATE trace_summaries SET
		span_count = (SELECT COUNT(*) FROM spans INDEXED BY idx_spans_trace WHERE project_id = ?1 AND trace_id = ?2),
		has_root = EXISTS (SELECT 1 FROM spans INDEXED BY idx_spans_trace
			WHERE project_id = ?1 AND trace_id = ?2
			  AND (parent_span_id IS NULL OR parent_span_id = '')),
		orphan_count = (SELECT COUNT(*) FROM spans s INDEXED BY idx_spans_trace
			WHERE s.project_id = ?1 AND s.trace_id = ?2
			  AND s.parent_span_id IS NOT NULL AND s.parent_span_id != ''
			  AND NOT EXISTS (SELECT 1 FROM spans p INDEXED BY idx_spans_trace
			                  WHERE p.project_id = ?1 AND p.trace_id = ?2
			                    AND p.span_id = s.parent_span_id))
		WHERE project_id = ?1 AND trace_id = ?2
		  AND EXISTS (SELECT 1 FROM spans INDEXED BY idx_spans_trace WHERE project_id = ?1 AND trace_id = ?2)`

	// clearStaleRootSQL drops root fields once the root span is gone, so a
	// rootless trace never lists a name taken from somewhere else.
	clearStaleRootSQL = `UPDATE trace_summaries
		SET root_name = '', root_service = '', root_duration_us = 0
		WHERE project_id = ?1 AND trace_id = ?2 AND has_root = 0 AND root_name != ''`

	// settleTraceStructureSQL resolves a summary whose spans are all gone: the
	// only evidence left is root_name, and there are no spans to be orphans.
	settleTraceStructureSQL = `UPDATE trace_summaries
		SET has_root = (root_name != ''), orphan_count = 0
		WHERE project_id = ?1 AND trace_id = ?2 AND has_root IS NULL`
)

// refreshTraceStructureTx recomputes span_count, has_root and orphan_count for
// each key from the spans table, inside tx. Callers run it after the span
// writes of the same transaction so the new rows are visible.
func refreshTraceStructureTx(ctx context.Context, tx *sql.Tx, keys []TraceKey) error {
	if len(keys) == 0 {
		return nil
	}
	var stmts [3]*sql.Stmt
	for i, q := range []string{refreshTraceStructureSQL, clearStaleRootSQL, settleTraceStructureSQL} {
		st, err := tx.PrepareContext(ctx, q)
		if err != nil {
			return err
		}
		defer st.Close()
		stmts[i] = st
	}
	for _, k := range keys {
		for _, st := range stmts {
			if _, err := st.ExecContext(ctx, k.ProjectID, k.TraceID); err != nil {
				return err
			}
		}
	}
	return nil
}

// RefreshTraceStructure recomputes the structural summary columns for keys in
// one transaction. It is the recompute-on-eviction hook: call it after spans of
// a still-listed trace were deleted.
func (r *Repository) RefreshTraceStructure(ctx context.Context, keys []TraceKey) error {
	if len(keys) == 0 {
		return nil
	}
	return r.execLow(FamilySpans, func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := refreshTraceStructureTx(ctx, tx, keys); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// BackfillTraceStructure fills has_root and orphan_count for summaries written
// before migration 035, at most max summaries per call. It reports whether rows
// remain, so the caller knows more cycles are needed. Each batch takes the
// write connection once through the low-priority queue and releases it, with
// the configured yield in between, so ingest and the WAL checkpoint keep moving.
func (r *Repository) BackfillTraceStructure(ctx context.Context, max int64) (int64, bool, error) {
	var total int64
	for total < max {
		if err := ctx.Err(); err != nil {
			return total, true, err
		}
		batch := int64(traceStructureBatch)
		if remaining := max - total; remaining < batch {
			batch = remaining
		}
		var n int64
		if err := r.execLow(FamilySpans, func(db *sql.DB) error {
			var e error
			n, e = r.backfillTraceStructureBatch(ctx, db, batch)
			return e
		}); err != nil {
			return total, true, err
		}
		total += n
		if n < batch {
			return total, false, nil
		}
		if r.deleteBatchYield > 0 {
			select {
			case <-ctx.Done():
				return total, true, ctx.Err()
			case <-time.After(r.deleteBatchYield):
			}
		}
	}
	return total, true, nil
}

// backfillTraceStructureBatch refreshes up to limit summaries that are still
// NULL, seeking the partial index idx_trace_summaries_unchecked.
func (r *Repository) backfillTraceStructureBatch(ctx context.Context, db *sql.DB, limit int64) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT project_id, trace_id FROM trace_summaries WHERE has_root IS NULL LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	var keys []TraceKey
	for rows.Next() {
		var k TraceKey
		if err := rows.Scan(&k.ProjectID, &k.TraceID); err != nil {
			rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := refreshTraceStructureTx(ctx, tx, keys); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(keys)), nil
}

// summaryKeysForSpans returns the distinct traces of a span batch.
func summaryKeysForSpans(sums []traceSummaryAgg) []TraceKey {
	keys := make([]TraceKey, len(sums))
	for i, s := range sums {
		keys[i] = TraceKey{ProjectID: s.projectID, TraceID: s.traceID}
	}
	return keys
}

// DeleteSpansByMaxIDRefreshing deletes spans up to maxID like DeleteSpansByMaxID
// and then refreshes the summaries of error traces that lose only some of their
// spans. Error summaries outlive their spans by design (they list until the
// error cutoff), so without this their span_count, has_root and root fields keep
// describing spans that are gone. Non-error summaries are deleted together with
// their spans by the retention cutoff and need no refresh.
func (r *Repository) DeleteSpansByMaxIDRefreshing(ctx context.Context, maxID int64) (int64, error) {
	keys, err := r.errorTraceKeysUpToID(ctx, maxID)
	if err != nil {
		return 0, err
	}
	n, err := r.DeleteSpansByMaxID(maxID)
	if err != nil {
		return n, err
	}
	return n, r.RefreshTraceStructure(ctx, keys)
}

// errorTraceKeysUpToID lists the traces among spans with id <= maxID that have
// an error summary. The id range is the primary key, so this is a bounded scan.
func (r *Repository) errorTraceKeysUpToID(ctx context.Context, maxID int64) ([]TraceKey, error) {
	qctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(qctx, `
		SELECT DISTINCT t.project_id, t.trace_id
		FROM (SELECT DISTINCT project_id, trace_id FROM spans WHERE id <= ?) s
		JOIN trace_summaries t ON t.project_id = s.project_id AND t.trace_id = s.trace_id
		WHERE t.has_error = 1`, maxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []TraceKey
	for rows.Next() {
		var k TraceKey
		if err := rows.Scan(&k.ProjectID, &k.TraceID); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
