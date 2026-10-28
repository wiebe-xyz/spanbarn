package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// evictCascadeTables lists the trace_id-keyed tables of the span family a
// victim trace is removed from, in delete order. spans and error_samples have a
// trace_id index; trace_summaries is keyed by (project_id, trace_id). Their
// deletes check the victim's summary again, so the summary goes last: a stop
// in between leaves the summary, and the next pass picks the trace again.
//
// evictCascadeOtherTables live in their own files in a split layout, and are
// deleted after the summaries, one write each, for the traces whose summary
// went. If the process dies in between, those rows outlive their trace until
// their own retention removes them; the trace itself is already gone from
// every list.
var (
	evictCascadeTables      = []string{"spans", "error_samples", "trace_summaries"}
	evictCascadeOtherTables = []string{"prompt_records", "logs"}
)

// evictChunk is how many rows one write job of the eviction deletes. A trace
// of project 81 on prod averages 65 spans and reaches 13,439, so one batch of
// victims spans tens of thousands of rows.
const evictChunk = 1000

// evictableSQL is true when the summary alias of project ?2 is one the
// eviction takes: no error, ingested before ?3, not pinned.
func evictableSQL(alias string) string {
	return alias + `.project_id = ?2 AND ` + alias + `.has_error = 0 AND ` + alias + `.ingested_at < ?3
		  AND NOT EXISTS (SELECT 1 FROM pinned_traces p
		                  WHERE p.project_id = ` + alias + `.project_id
		                    AND p.trace_id = ` + alias + `.trace_id)`
}

// evictTraceIndex names the trace_id index of the tables keyed by trace_id
// alone.
var evictTraceIndex = map[string]string{
	"spans":         "idx_spans_trace",
	"error_samples": "idx_error_samples_trace",
}

// evictRowsSQL deletes up to ?4 rows of table that belong to the traces of
// project ?2 listed in the JSON array ?1 and whose summary the eviction still
// takes. The check runs again inside the write, because the victims were read
// outside it: a trace can have gained an error or a pin since. The summaries
// delete returns the trace ids it removed.
//
// The CROSS JOIN keeps the id list as the outer loop, and spans and
// error_samples are sought through their trace_id index. Left to the planner,
// they were walked by project_id, all of the project's rows, and filtered
// against the list.
func evictRowsSQL(table string) string {
	if table == "trace_summaries" {
		return `DELETE FROM trace_summaries WHERE rowid IN (
			SELECT t.rowid FROM json_each(?1) j
			CROSS JOIN trace_summaries t ON t.project_id = ?2 AND t.trace_id = j.value
			WHERE ` + evictableSQL("t") + `
			LIMIT ?4)
			RETURNING trace_id`
	}
	return `DELETE FROM ` + table + ` WHERE rowid IN (
		SELECT x.rowid FROM json_each(?1) j
		CROSS JOIN ` + table + ` x INDEXED BY ` + evictTraceIndex[table] + ` ON x.trace_id = j.value
		WHERE x.project_id = ?2
		  AND EXISTS (SELECT 1 FROM trace_summaries t
		              WHERE t.trace_id = x.trace_id AND ` + evictableSQL("t") + `)
		LIMIT ?4)
		RETURNING trace_id`
}

// EvictProjectTracesOlderThan enforces a per-project retention cap by deleting a
// project's NON-ERROR, non-pinned traces whose ingested_at is before cutoff,
// cascading across every trace_id-keyed table. Error traces are never evicted
// (they remain the health signal), pinned traces are protected, and metrics are
// untouched (aggregates are rolled up at ingest, independent of raw-trace
// storage). It returns the number of traces it evicted.
//
// Each batch of up to retentionDeleteBatch victims is read outside the write
// slot (on the read pool when there is one), and each table's rows are
// deleted evictChunk at a time, one write job per chunk. One job used to read
// the victims and delete them from every span-family table, and on prod it
// held the write slot 11.5s (2026-10-28).
func (r *Repository) EvictProjectTracesOlderThan(ctx context.Context, projectID int64, cutoff time.Time) (int64, error) {
	c := cutoff.UTC()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		victims, err := r.evictVictims(ctx, projectID, c)
		if err != nil || len(victims) == 0 {
			return total, err
		}
		evicted, err := r.evictTraces(ctx, projectID, c, victims)
		if err != nil {
			return total, err
		}
		if err := r.deleteOtherTraceRows(ctx, evicted); err != nil {
			return total, err
		}
		total += int64(len(evicted))
		if len(victims) < retentionDeleteBatch {
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

// evictVictimsSQL reads up to ?1 trace ids of project ?2 the eviction takes,
// oldest first.
var evictVictimsSQL = `
	SELECT t.trace_id FROM trace_summaries t
	WHERE ` + evictableSQL("t") + `
	ORDER BY t.ingested_at
	LIMIT ?1`

// evictVictims reads up to retentionDeleteBatch trace ids of projectID the
// eviction takes, oldest first.
func (r *Repository) evictVictims(ctx context.Context, projectID int64, cutoff time.Time) ([]any, error) {
	var q rowQuerier = r.db
	if r.logTrimReads != nil {
		q = r.logTrimReads
	}
	return queryStrings(ctx, q, evictVictimsSQL, retentionDeleteBatch, projectID, cutoff)
}

// evictTraces deletes the victims' rows from evictCascadeTables, a chunk per
// write job, and returns the trace ids whose summary it deleted.
func (r *Repository) evictTraces(ctx context.Context, projectID int64, cutoff time.Time, victims []any) ([]any, error) {
	payload, err := json.Marshal(victims)
	if err != nil {
		return nil, err
	}
	var evicted []any
	for _, table := range evictCascadeTables {
		query, label := evictRowsSQL(table), "EvictProjectTracesOlderThan:"+table
		for {
			var ids []any
			err := r.submit(FamilySpans, writescheduler.Low, label, func(db *sql.DB) error {
				var e error
				ids, e = queryStrings(ctx, db, query, string(payload), projectID, cutoff, evictChunk)
				return e
			})
			if err != nil {
				return evicted, err
			}
			if table == "trace_summaries" {
				evicted = append(evicted, ids...)
			}
			if len(ids) < evictChunk {
				break
			}
		}
	}
	return evicted, nil
}

// queryStrings runs query and returns its single text column.
func queryStrings(ctx context.Context, db rowQuerier, query string, args ...any) ([]any, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []any
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// deleteOtherTraceRows removes ids from the tables outside the span family.
func (r *Repository) deleteOtherTraceRows(ctx context.Context, ids []any) error {
	if len(ids) == 0 {
		return nil
	}
	q := " WHERE trace_id IN (" + placeholderList(len(ids)) + ")"
	for _, table := range evictCascadeOtherTables {
		if err := r.execLow(TableFamily(table), func(db *sql.DB) error {
			// Main drops a sharded family's table once it is empty.
			if has, err := hasTable(ctx, db, table); err != nil || !has {
				return err
			}
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
