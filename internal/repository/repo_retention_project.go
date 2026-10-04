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
// trace_summaries is keyed by (project_id, trace_id).
//
// The span-family tables go in the transaction that picks the victims. The
// others live in their own files in a split layout, and a transaction cannot
// commit two files atomically, so they are deleted after it, one write each.
// If the process dies in between, those rows outlive their trace until their
// own retention removes them; the trace itself is already gone from every list.
var (
	evictCascadeTables      = []string{"spans", "error_samples", "trace_summaries"}
	evictCascadeOtherTables = []string{"prompt_records", "logs"}
)

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
		var ids []any
		if err := r.execLow(FamilySpans, func(db *sql.DB) error {
			var e error
			ids, e = r.evictTraceBatch(ctx, db, projectID, c)
			return e
		}); err != nil {
			return total, err
		}
		if err := r.deleteOtherTraceRows(ctx, ids); err != nil {
			return total, err
		}
		total += int64(len(ids))
		if len(ids) < retentionDeleteBatch {
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

// deleteOtherTraceRows removes ids from the tables outside the span family.
func (r *Repository) deleteOtherTraceRows(ctx context.Context, ids []any) error {
	if len(ids) == 0 {
		return nil
	}
	q := " WHERE trace_id IN (" + placeholderList(len(ids)) + ")"
	for _, table := range evictCascadeOtherTables {
		if err := r.execLow(TableFamily(table), func(db *sql.DB) error {
			_, err := db.ExecContext(ctx, "DELETE FROM "+table+q, ids...)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func placeholderList(n int) string {
	p := strings.Repeat("?,", n)
	return p[:len(p)-1]
}

// evictTraceBatch selects up to retentionDeleteBatch victim trace_ids and deletes
// them across evictCascadeTables in one transaction. Returns the evicted trace
// ids (none means nothing was eligible).
func (r *Repository) evictTraceBatch(ctx context.Context, db *sql.DB, projectID int64, cutoff time.Time) ([]any, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	var ids []any
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	q := " WHERE trace_id IN (" + placeholderList(len(ids)) + ")"
	for _, table := range evictCascadeTables {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+q, ids...); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
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
