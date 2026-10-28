package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// The error-sample delete works like the log trim: it reads a window of a
// project's samples copied before the cutoff in (project_id, sampled_at) index
// order, then deletes the window's unprotected rows by rowid. A sample is
// protected while error_exemplars lists its trace.
//
// It used one DELETE per batch with the scan inside it. Each batch walked
// every protected sample from the oldest again, and on prod one batch held the
// write slot for 6 to 17s in steady hours (2026-10-28), cold page reads of the
// scan and of the rows it deleted. Reading the window on the read pool first
// takes those reads out of the slot: the window also reads each candidate's
// payload and its trace index entry, the pages the DELETE changes.

// errorSampleWindowSQL reads one window of a project's samples copied before
// the cutoff, each with whether an exemplar keeps its trace. The last two
// columns are read only to load the row's overflow pages and its
// idx_error_samples_trace entry into the page cache.
func errorSampleWindowSQL(resume bool) string {
	after := ""
	if resume {
		after = ` AND (e.sampled_at, e.rowid) > (?3, ?4)`
	}
	return `
		SELECT e.rowid, CAST(e.sampled_at AS TEXT),
		       EXISTS (SELECT 1 FROM error_exemplars x
		               WHERE x.project_id = e.project_id AND x.trace_id = e.trace_id),
		       length(e.attributes) + length(e.events),
		       EXISTS (SELECT 1 FROM error_samples t INDEXED BY idx_error_samples_trace
		               WHERE t.trace_id = e.trace_id AND t.rowid = e.rowid)
		FROM error_samples e INDEXED BY idx_error_samples_project
		WHERE e.project_id = ?1 AND e.sampled_at < ?2` + after + `
		ORDER BY e.sampled_at, e.rowid
		LIMIT ?5`
}

// deleteErrorSampleRowsSQL deletes the listed rows and checks each one again:
// when the window was read outside the write slot, a trace can have become an
// exemplar since, and a row id can have been reused by a newer row.
const deleteErrorSampleRowsSQL = `DELETE FROM error_samples
	WHERE rowid IN (SELECT value FROM json_each(?1))
	  AND project_id = ?2 AND sampled_at < ?3
	  AND NOT EXISTS (SELECT 1 FROM error_exemplars x
	                  WHERE x.project_id = error_samples.project_id
	                    AND x.trace_id = error_samples.trace_id)`

// scanErrorSampleWindow reads up to limit samples of one project copied
// before cutoff, starting after cur.
func scanErrorSampleWindow(
	ctx context.Context, q rowQuerier, pid int64, cutoff time.Time, cur logTrimCursor, limit int,
) (logTrimWindow, error) {
	var w logTrimWindow
	args := []any{pid, cutoff, cur.at, cur.rowid, limit}
	rows, err := q.QueryContext(ctx, errorSampleWindowSQL(cur.set), args...)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	for rows.Next() {
		var protected, indexed bool
		var size sql.NullInt64
		if err := rows.Scan(&w.lastID, &w.lastAt, &protected, &size, &indexed); err != nil {
			return w, err
		}
		w.scanned++
		if protected {
			w.protected = append(w.protected, w.lastID)
		} else {
			w.ids = append(w.ids, w.lastID)
		}
	}
	if err := rows.Err(); err != nil {
		return w, err
	}
	return w, rows.Close()
}

// deleteErrorSampleRows deletes the samples of ids whose trace is still not
// an exemplar.
func deleteErrorSampleRows(ctx context.Context, db *sql.DB, pid int64, cutoff time.Time, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, deleteErrorSampleRowsSQL, string(payload), pid, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// errorSampleDeleteLabel keeps the label the batched delete had, so slow
// writes before and after the change group under one op.
var errorSampleDeleteLabel = "DeleteErrorSamplesOlderThan:" + FamilySpans.String()

// deleteErrorSampleWindow deletes the unprotected samples of one window of
// one project and reports whether samples before the cutoff remain. With a
// read pool the window is read there and the write slot holds only the
// DELETE; without one both run in the slot.
func (r *Repository) deleteErrorSampleWindow(ctx context.Context, pid int64, cutoff time.Time, cur *logTrimCursor) (int64, bool, error) {
	var n int64
	if r.logTrimReads == nil {
		var w logTrimWindow
		err := r.submit(FamilySpans, writescheduler.Low, errorSampleDeleteLabel, func(db *sql.DB) error {
			var e error
			if w, e = scanErrorSampleWindow(ctx, db, pid, cutoff, *cur, retentionDeleteBatch); e != nil {
				return e
			}
			n, e = deleteErrorSampleRows(ctx, db, pid, cutoff, w.ids)
			return e
		})
		if err != nil {
			return n, false, err
		}
		return n, w.advance(cur, retentionDeleteBatch), nil
	}
	w, err := scanErrorSampleWindow(ctx, r.logTrimReads, pid, cutoff, *cur, retentionDeleteBatch)
	if err != nil {
		return 0, false, err
	}
	if len(w.ids) > 0 {
		err = r.submit(FamilySpans, writescheduler.Low, errorSampleDeleteLabel, func(db *sql.DB) error {
			var e error
			n, e = deleteErrorSampleRows(ctx, db, pid, cutoff, w.ids)
			return e
		})
		if err != nil {
			return n, false, err
		}
	}
	return n, w.advance(cur, retentionDeleteBatch), nil
}

// DeleteErrorSamplesOlderThan deletes error samples copied before cutoff,
// except those of a trace listed in error_exemplars: they stay until
// PruneErrorExemplars drops the trace's exemplar rows. Each project resumes
// from the cursor the previous pass left, so the protected samples a pass has
// walked are not read again; PruneErrorExemplars resets the cursors when it
// drops exemplars, so their samples go on the next pass.
func (r *Repository) DeleteErrorSamplesOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	pids, err := r.distinctProjectIDs(ctx, "error_samples")
	if err != nil {
		return 0, err
	}
	var total int64
	for _, pid := range pids {
		n, err := r.trimByWindows(ctx, r.errorSampleTrim, "error_samples:"+strconv.FormatInt(pid, 10),
			func(cur *logTrimCursor) (int64, bool, error) {
				return r.deleteErrorSampleWindow(ctx, pid, cutoff, cur)
			})
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
