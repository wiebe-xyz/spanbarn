package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// The boring-span expiry reads a chunk of expired spans off the write slot and
// then runs three kinds of write job: the span DELETE by rowid, jobs that drop
// the summaries of the chunk's traces with no span left, boringSummaryChunk
// traces each, and the structure refresh of the traces that keep spans, a few
// traces per job.
//
// It used one job per 1000 spans that deleted them and pruned and refreshed
// every touched trace in the same transaction. 1000 expiring spans belong to
// about 366 traces on prod, so that job ran some 1,400 summary statements
// beside the delete, and one held the spans writer for 5.6 s (2026-10-28).

// boringExpiryChunk is how many expired spans one delete job removes.
const boringExpiryChunk = retentionDeleteBatch

// boringExpiryLabel keeps the label the batched delete had, so slow writes
// before and after the change group under one op.
var boringExpiryLabel = "DeleteExpiredBoringSpans:" + FamilySpans.String()

// boringSummaryLabel names the job that drops the chunk's spanless summaries,
// so its time reads apart from the span delete's.
const boringSummaryLabel = "DeleteExpiredBoringSpans:summaries"

// boringVictimsSQL reads the oldest expired spans from the partial
// idx_spans_expires index, which covers expires_at and the rowid; the project
// and trace columns cost one row lookup per span.
const boringVictimsSQL = `SELECT rowid, project_id, trace_id FROM spans INDEXED BY idx_spans_expires
	WHERE expires_at IS NOT NULL AND expires_at < ?1
	ORDER BY expires_at
	LIMIT ?2`

// deleteBoringRowsSQL deletes the listed spans and checks each one again: when
// the chunk was read outside the write slot, a row id can have been reused by
// a newer span.
const deleteBoringRowsSQL = `DELETE FROM spans
	WHERE rowid IN (SELECT value FROM json_each(?1))
	  AND expires_at IS NOT NULL AND expires_at < ?2`

// DeleteExpiredBoringSpans deletes sampled-boring spans whose stamped expires_at
// has passed. Classification stamps expires_at at storage time, so cleanup is a
// bounded seek of the partial idx_spans_expires index. Interesting spans carry
// a NULL expires_at and are removed by the aggregate-then-delete pass instead;
// pre-migration rows are also NULL and drain that same way.
//
// A trace can mix expiring spans with spans kept longer (its spans arrived in
// batches that were classified apart), so its summary goes with its last span
// and is refreshed while spans remain. The summary work runs after the span
// delete, in jobs of its own; a stop between them leaves a summary that the
// trace summaries' own expiry or the next refresh of the trace settles.
func (r *Repository) DeleteExpiredBoringSpans(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.UTC()
	var total int64
	for first := true; ; first = false {
		if !first && !r.yieldBetweenBatches(ctx) {
			return total, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
		ids, keys, err := r.boringVictims(ctx, cutoff)
		if err != nil || len(ids) == 0 {
			return total, err
		}
		n, err := r.deleteBoringChunk(ctx, ids, keys, cutoff)
		total += n
		if err != nil || n == 0 || len(ids) < boringExpiryChunk {
			return total, err
		}
	}
}

// yieldBetweenBatches waits deleteBatchYield, so the WAL checkpoint and the
// readers get a turn between delete jobs. It reports false when ctx ended.
func (r *Repository) yieldBetweenBatches(ctx context.Context) bool {
	if r.deleteBatchYield <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(r.deleteBatchYield):
		return true
	}
}

// boringVictims reads up to boringExpiryChunk expired spans, oldest expiry
// first, on the read pool when there is one, and the distinct traces they
// belong to.
func (r *Repository) boringVictims(ctx context.Context, cutoff time.Time) ([]int64, []TraceKey, error) {
	var q rowQuerier = r.db
	if r.logTrimReads != nil {
		q = r.logTrimReads
	}
	rows, err := q.QueryContext(ctx, boringVictimsSQL, cutoff, boringExpiryChunk)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []int64
	var keys []TraceKey
	seen := make(map[TraceKey]bool)
	for rows.Next() {
		var id int64
		var k TraceKey
		if err := rows.Scan(&id, &k.ProjectID, &k.TraceID); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return ids, keys, rows.Close()
}

// deleteBoringChunk deletes one chunk of expired spans in one job, then drops
// the summaries of its traces with no span left in jobs of their own and
// refreshes the traces that keep spans through RefreshTraceStructure.
func (r *Repository) deleteBoringChunk(ctx context.Context, ids []int64, keys []TraceKey, cutoff time.Time) (int64, error) {
	n, err := r.submitRows(FamilySpans, writescheduler.Low, boringExpiryLabel, func(db *sql.DB) (int64, error) {
		return deleteRowsByID(ctx, db, deleteBoringRowsSQL, ids, cutoff)
	})
	if err != nil || n == 0 {
		return n, err
	}
	kept, err := r.dropSpanlessSummariesInParts(ctx, keys)
	if err != nil {
		return n, err
	}
	return n, r.RefreshTraceStructure(ctx, kept)
}

// boringSummaryChunk is how many traces one summary job checks. A check is
// three index seeks, and on prod's cold disk one job over a whole chunk's 366
// traces held the spans writer for 2,043 ms (2026-10-28 17:01), 5.6 ms a trace.
const boringSummaryChunk = 100

// boringSummaryWarmSQL reads the pages the summary delete seeks: the summary
// row in the table (span_count is outside the key index, so the read reaches
// the row), the trace's spans in idx_spans_trace and its error samples.
const boringSummaryWarmSQL = `SELECT
	(SELECT span_count FROM trace_summaries WHERE project_id = ?1 AND trace_id = ?2),
	EXISTS (SELECT 1 FROM spans INDEXED BY idx_spans_trace WHERE project_id = ?1 AND trace_id = ?2),
	EXISTS (SELECT 1 FROM error_samples WHERE project_id = ?1 AND trace_id = ?2)`

// dropSpanlessSummariesInParts drops the spanless summaries of keys
// boringSummaryChunk traces per job, each part warmed on the read pool first so
// the job finds its pages cached. It returns the keys whose summary stays. A
// stop between parts leaves the later summaries for the next cycle.
func (r *Repository) dropSpanlessSummariesInParts(ctx context.Context, keys []TraceKey) ([]TraceKey, error) {
	var kept []TraceKey
	for _, part := range chunkTraceKeys(keys, boringSummaryChunk) {
		if err := ctx.Err(); err != nil {
			return kept, err
		}
		r.warmSummaryKeys(ctx, part)
		var stay []TraceKey
		if _, err := r.submitRows(FamilySpans, writescheduler.Low, boringSummaryLabel, func(db *sql.DB) (int64, error) {
			var e error
			if stay, e = dropSpanlessSummaries(ctx, db, part); e != nil {
				return 0, e
			}
			return int64(len(part) - len(stay)), nil
		}); err != nil {
			return kept, err
		}
		kept = append(kept, stay...)
	}
	return kept, nil
}

// warmSummaryKeys runs boringSummaryWarmSQL for each key on the read pool. It
// only fills the page cache, so on a failed read it stops and the job seeks the
// pages itself.
func (r *Repository) warmSummaryKeys(ctx context.Context, keys []TraceKey) {
	if r.logTrimReads == nil {
		return
	}
	var spanCount sql.NullInt64
	var hasSpans, hasSamples bool
	for _, k := range keys {
		row := r.logTrimReads.QueryRowContext(ctx, boringSummaryWarmSQL, k.ProjectID, k.TraceID)
		if row.Scan(&spanCount, &hasSpans, &hasSamples) != nil {
			return
		}
	}
}

// dropSpanlessSummaries deletes the summaries of keys whose trace has no span
// left and returns the keys whose summary stays.
func dropSpanlessSummaries(ctx context.Context, db *sql.DB, keys []TraceKey) ([]TraceKey, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	st, err := tx.PrepareContext(ctx, deleteSpanlessSummarySQL)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	var kept []TraceKey
	for _, k := range keys {
		res, err := st.ExecContext(ctx, k.ProjectID, k.TraceID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			kept = append(kept, k)
		}
	}
	return kept, tx.Commit()
}
