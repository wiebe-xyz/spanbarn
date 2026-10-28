package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// windowDelete is a retention delete that reads a window of one project's
// rows before the cutoff in index order and deletes the window's unprotected
// rows by rowid. With a read pool the window is read there and the write slot
// holds only the DELETE; without one both run in the slot. The error-sample
// delete and the trace-summary delete use it.
type windowDelete struct {
	family Family
	label  string
	// scan reads one window starting after cur.
	scan func(q rowQuerier, cur logTrimCursor) (logTrimWindow, error)
	// del deletes the rows of ids that are still unprotected.
	del func(db *sql.DB, ids []int64) (int64, error)
}

// run deletes one window and reports whether rows before the cutoff remain.
func (d windowDelete) run(r *Repository, cur *logTrimCursor) (int64, bool, error) {
	var n int64
	if r.logTrimReads == nil {
		var w logTrimWindow
		err := r.submit(d.family, writescheduler.Low, d.label, func(db *sql.DB) error {
			var e error
			if w, e = d.scan(db, *cur); e != nil {
				return e
			}
			n, e = d.del(db, w.ids)
			return e
		})
		if err != nil {
			return n, false, err
		}
		return n, w.advance(cur, retentionDeleteBatch), nil
	}
	w, err := d.scan(r.logTrimReads, *cur)
	if err != nil {
		return 0, false, err
	}
	if len(w.ids) > 0 {
		err = r.submit(d.family, writescheduler.Low, d.label, func(db *sql.DB) error {
			var e error
			n, e = d.del(db, w.ids)
			return e
		})
		if err != nil {
			return n, false, err
		}
	}
	return n, w.advance(cur, retentionDeleteBatch), nil
}

// deleteByProject runs the window delete newDelete builds for each project of
// table, each from the cursor that cursors holds for the project.
func (r *Repository) deleteByProject(
	ctx context.Context, table string, cursors *logTrimCursors, newDelete func(pid int64) windowDelete,
) (int64, error) {
	pids, err := r.distinctProjectIDs(ctx, table)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, pid := range pids {
		d := newDelete(pid)
		n, err := r.trimByWindows(ctx, cursors, logTrimKey(table, pid),
			func(cur *logTrimCursor) (int64, bool, error) { return d.run(r, cur) })
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// scanWindow runs a window query. Its first three columns are the row id, the
// stored time text and whether the row is protected; the columns after them
// are read only to load the pages the DELETE changes into the page cache.
func scanWindow(ctx context.Context, q rowQuerier, query string, args ...any) (logTrimWindow, error) {
	var w logTrimWindow
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return w, err
	}
	var protected bool
	dest := []any{&w.lastID, &w.lastAt, &protected}
	for len(dest) < len(cols) {
		dest = append(dest, new(any))
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
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

// deleteRowsByID runs query, a DELETE that takes the JSON array of ids as ?1
// and args from ?2 on, and returns the rows it deleted.
func deleteRowsByID(ctx context.Context, db *sql.DB, query string, ids []int64, args ...any) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, query, append([]any{string(payload)}, args...)...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
