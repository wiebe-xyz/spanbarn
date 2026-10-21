package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// logTrimCursor marks how far one project's trim has scanned the
// (project_id, ingested_at) index. Rows before it were either deleted or are
// protected (pinned trace, recent error sample), so the next batch resumes after
// it instead of walking over the protected rows again.
type logTrimCursor struct {
	set        bool
	ingestedAt string // the stored text, so the comparison never depends on how the driver formats times
	rowid      int64
	// started is when the scan began at the start of the index.
	started time.Time
}

// logTrimCursorMaxAge is how long a cursor carries over between retention
// passes. A protected row stops being protected when its error sample expires
// or its pin is removed, and kept_logs takes rows with old ingest times when a
// logs shard expires; the cursor has passed both. Starting over every hour
// deletes them at most an hour late, and passes in between skip the protected
// rows already scanned.
const logTrimCursorMaxAge = time.Hour

// logTrimCursors holds each (table, project) cursor between retention passes.
type logTrimCursors struct {
	mu  sync.Mutex
	cur map[string]logTrimCursor
}

func newLogTrimCursors() *logTrimCursors {
	return &logTrimCursors{cur: map[string]logTrimCursor{}}
}

func logTrimKey(table string, pid int64) string {
	return table + ":" + strconv.FormatInt(pid, 10)
}

// load returns the stored cursor, or a fresh one when there is none or it is
// older than logTrimCursorMaxAge. A nil store always starts fresh.
func (c *logTrimCursors) load(key string, now time.Time) logTrimCursor {
	if c == nil {
		return logTrimCursor{started: now}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.cur[key]
	if !ok || now.Sub(cur.started) >= logTrimCursorMaxAge {
		return logTrimCursor{started: now}
	}
	return cur
}

func (c *logTrimCursors) store(key string, cur logTrimCursor) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur[key] = cur
}

// logProtectedSQL is true for a row of table that retention keeps: its trace
// is pinned, or it has an error sample newer than the errorLogCutoff bound to
// the placeholder sampledAfter.
func logProtectedSQL(table, sampledAfter string) string {
	return table + `.trace_id IS NOT NULL AND (
		           EXISTS (
		               SELECT 1 FROM pinned_traces p
		               WHERE p.project_id = ` + table + `.project_id
		                 AND p.trace_id = ` + table + `.trace_id
		           )
		           OR EXISTS (
		               SELECT 1 FROM error_samples e
		               WHERE e.trace_id = ` + table + `.trace_id
		                 AND e.sampled_at > ` + sampledAfter + `
		           )
		       )`
}

// logTrimSelectSQL reads one window of rows older than the cutoff in index
// order, each with whether it is protected. The cursor clause is added once
// the scan has moved past the start of the index. Bounding the window by rows
// scanned keeps a batch short however many protected rows a project holds: a
// window bounded by deletable rows found scans every protected row in one
// write slot when nothing is deletable.
func logTrimSelectSQL(table string, resume bool) string {
	after := ""
	if resume {
		after = ` AND (` + table + `.ingested_at, ` + table + `.rowid) > (?3, ?4)`
	}
	return `
		SELECT ` + table + `.rowid, CAST(` + table + `.ingested_at AS TEXT),
		       ` + logProtectedSQL(table, "?5") + `
		FROM ` + table + `
		WHERE ` + table + `.project_id = ?1 AND ` + table + `.ingested_at < ?2` + after + `
		ORDER BY ` + table + `.ingested_at, ` + table + `.rowid
		LIMIT ?6`
}

// logTrimDeleteSQL deletes the listed rows and checks each one again: when the
// window was read outside the write slot, a row can have been pinned or given
// an error sample since, and a row id can have been reused by a newer row.
func logTrimDeleteSQL(table string) string {
	return `DELETE FROM ` + table + `
		WHERE rowid IN (SELECT value FROM json_each(?1))
		  AND project_id = ?2 AND ingested_at < ?3
		  AND NOT (` + logProtectedSQL(table, "?4") + `)`
}

// logTrimWindow is one scanned window: the unprotected row ids and the last
// row scanned.
type logTrimWindow struct {
	ids     []int64
	scanned int
	lastAt  string
	lastID  int64
}

// advance moves cur past the window and reports whether rows before the
// cutoff remain.
func (w logTrimWindow) advance(cur *logTrimCursor, limit int) (more bool) {
	if w.scanned == 0 {
		return false
	}
	cur.set, cur.ingestedAt, cur.rowid = true, w.lastAt, w.lastID
	return w.scanned == limit
}

// scanLogTrimWindow reads up to limit rows of one project that were ingested
// before cutoff, starting after cur.
func scanLogTrimWindow(
	ctx context.Context, db *sql.DB, table string, pid int64, cutoff, errorLogCutoff time.Time,
	cur logTrimCursor, limit int,
) (logTrimWindow, error) {
	var w logTrimWindow
	rows, err := db.QueryContext(ctx, logTrimSelectSQL(table, cur.set),
		pid, cutoff, cur.ingestedAt, cur.rowid, errorLogCutoff, limit)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	for rows.Next() {
		var protected bool
		if err := rows.Scan(&w.lastID, &w.lastAt, &protected); err != nil {
			return w, err
		}
		w.scanned++
		if !protected {
			w.ids = append(w.ids, w.lastID)
		}
	}
	if err := rows.Err(); err != nil {
		return w, err
	}
	return w, rows.Close()
}

// deleteLogTrimRows deletes the rows of ids that are still unprotected.
func deleteLogTrimRows(
	ctx context.Context, db *sql.DB, table string, pid int64, cutoff, errorLogCutoff time.Time, ids []int64,
) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, logTrimDeleteSQL(table), string(payload), pid, cutoff, errorLogCutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// trimLogsBatch scans one window on db and deletes its unprotected rows on
// the same handle, then moves the cursor past the last row it scanned. more is
// false once the scan reached the cutoff.
func trimLogsBatch(
	ctx context.Context, db *sql.DB, table string, pid int64, cutoff, errorLogCutoff time.Time,
	cur *logTrimCursor, limit int,
) (deleted int64, more bool, err error) {
	w, err := scanLogTrimWindow(ctx, db, table, pid, cutoff, errorLogCutoff, *cur, limit)
	if err != nil {
		return 0, false, err
	}
	deleted, err = deleteLogTrimRows(ctx, db, table, pid, cutoff, errorLogCutoff, w.ids)
	if err != nil {
		return 0, false, err
	}
	return deleted, w.advance(cur, limit), nil
}

// SetLogTrimReads gives the log trim a read-only pool for its window reads.
// With it, each batch reads its window outside the write slot and holds the
// slot only for the DELETE. Without it the read runs inside the slot on the
// writer, where one window of cold pages held the slot for 31s and the
// watchdog reported a wedge (#360).
func (r *Repository) SetLogTrimReads(db *sql.DB) { r.logTrimReads = db }

// trimLogsWindow trims one window of one project's rows and reports whether
// rows before the cutoff remain.
func (r *Repository) trimLogsWindow(
	ctx context.Context, table string, pid int64, cutoff, errorLogCutoff time.Time, cur *logTrimCursor,
) (int64, bool, error) {
	f := TableFamily(table)
	label := "deleteLogsFrom:" + f.String()
	var n int64
	if r.logTrimReads == nil {
		var more bool
		err := r.submit(f, writescheduler.Low, label, func(db *sql.DB) error {
			var e error
			n, more, e = trimLogsBatch(ctx, db, table, pid, cutoff, errorLogCutoff, cur, retentionDeleteBatch)
			return e
		})
		return n, more, err
	}
	w, err := scanLogTrimWindow(ctx, r.logTrimReads, table, pid, cutoff, errorLogCutoff, *cur, retentionDeleteBatch)
	if err != nil {
		return 0, false, err
	}
	if len(w.ids) > 0 {
		err = r.submit(f, writescheduler.Low, label, func(db *sql.DB) error {
			var e error
			n, e = deleteLogTrimRows(ctx, db, table, pid, cutoff, errorLogCutoff, w.ids)
			return e
		})
		if err != nil {
			return n, false, err
		}
	}
	return n, w.advance(cur, retentionDeleteBatch), nil
}

// trimLogsFrom trims one project's rows of table, a window per low-priority
// write, resuming from the cursor the previous pass left.
func (r *Repository) trimLogsFrom(ctx context.Context, table string, pid int64, cutoff, errorLogCutoff time.Time) (int64, error) {
	key := logTrimKey(table, pid)
	cur := r.logTrim.load(key, time.Now())
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, more, err := r.trimLogsWindow(ctx, table, pid, cutoff, errorLogCutoff, &cur)
		total += n
		if err != nil {
			return total, err
		}
		r.logTrim.store(key, cur)
		if !more {
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
