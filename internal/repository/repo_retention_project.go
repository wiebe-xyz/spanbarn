package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// evictCascadeTables lists the trace_id-keyed tables a victim trace is removed
// from. spans/error_samples/prompt_records/logs each have a trace_id index;
// trace_summaries is keyed by (project_id, trace_id). Order doesn't matter — the
// whole set is deleted in one transaction.
var evictCascadeTables = []string{"spans", "error_samples", "prompt_records", "logs", "trace_summaries"}

// EvictProjectTracesOlderThan enforces a per-project retention cap by deleting a
// project's NON-ERROR, non-pinned traces whose ingested_at is before cutoff,
// cascading across every trace_id-keyed table. Error traces are never evicted
// (they remain the health signal), pinned traces are protected, and metrics are
// untouched (aggregates are rolled up at ingest, independent of raw-trace
// storage). Batched like the other retention deletes so the single write
// connection is released — and the WAL checkpoint/readers un-starved — between
// batches.
func (r *Repository) EvictProjectTracesOlderThan(ctx context.Context, projectID int64, cutoff time.Time) (int64, error) {
	c := cutoff.UTC()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var deleted int64
		if err := r.execLow(func() error {
			n, e := r.evictTraceBatch(ctx, projectID, c)
			deleted = n
			return e
		}); err != nil {
			return total, err
		}
		total += deleted
		if deleted < retentionDeleteBatch {
			return total, nil
		}
		if r.deleteBatchYield > 0 {
			select {
			case <-ctx.Done():
				return total, ctx.Err()
			case <-time.After(r.deleteBatchYield):
			}
		}
	}
}

// evictTraceBatch selects up to retentionDeleteBatch victim trace_ids and deletes
// them across evictCascadeTables in one transaction. Returns the number of traces
// evicted (0 means nothing was eligible).
func (r *Repository) evictTraceBatch(ctx context.Context, projectID int64, cutoff time.Time) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT trace_id FROM trace_summaries
		WHERE project_id = ? AND has_error = 0 AND ingested_at < ?
		  AND NOT EXISTS (SELECT 1 FROM pinned_traces p
		                  WHERE p.project_id = trace_summaries.project_id
		                    AND p.trace_id  = trace_summaries.trace_id)
		ORDER BY ingested_at
		LIMIT ?`, projectID, cutoff, retentionDeleteBatch)
	if err != nil {
		return 0, err
	}
	var ids []any
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	for _, table := range evictCascadeTables {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE trace_id IN ("+placeholders+")", ids...); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}

// ProjectNonErrorTraceCountCutoff returns the ingested_at boundary that keeps the
// newest keepN non-error traces for a project: traces at or before the cutoff are
// beyond the cap. ok is false when the project has keepN or fewer non-error traces
// (nothing to evict). Count-based eviction composes this with
// EvictProjectTracesOlderThan(cutoff).
func (r *Repository) ProjectNonErrorTraceCountCutoff(ctx context.Context, projectID int64, keepN int) (time.Time, bool, error) {
	if keepN <= 0 {
		return time.Time{}, false, nil
	}
	// Offset keepN-1 lands on the keepN-th newest non-error trace; evicting
	// strictly older than its ingested_at keeps at least keepN (ties at the
	// boundary are kept).
	var cutoff time.Time
	err := r.db.QueryRowContext(ctx, `
		SELECT ingested_at FROM trace_summaries
		WHERE project_id = ? AND has_error = 0
		ORDER BY ingested_at DESC
		LIMIT 1 OFFSET ?`, projectID, keepN-1).Scan(&cutoff)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return cutoff, true, nil
}
