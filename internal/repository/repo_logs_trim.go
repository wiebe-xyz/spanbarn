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

// logTrimSelectSQL reads one window of rows older than the cutoff in index
// order, each with whether it is protected. The cursor clause is added once
// the scan has moved past the start of the index. Bounding the window by rows
// scanned keeps a batch short however many protected rows a project holds: a
// window bounded by deletable rows found scans every protected row in one
// write slot when nothing is deletable.
func logTrimSelectSQL(table string, resume bool) string {
	after := ""
	if resume {
		after = ` AND (l.ingested_at, l.rowid) > (?3, ?4)`
	}
	return `
		SELECT l.rowid, CAST(l.ingested_at AS TEXT),
		       l.trace_id IS NOT NULL AND (
		           EXISTS (
		               SELECT 1 FROM pinned_traces p
		               WHERE p.project_id = l.project_id
		                 AND p.trace_id = l.trace_id
		           )
		           OR EXISTS (
		               SELECT 1 FROM error_samples e
		               WHERE e.trace_id = l.trace_id
		                 AND e.sampled_at > ?5
		           )
		       )
		FROM ` + table + ` l
		WHERE l.project_id = ?1 AND l.ingested_at < ?2` + after + `
		ORDER BY l.ingested_at, l.rowid
		LIMIT ?6`
}

// trimLogsBatch scans up to limit rows of one project that were ingested
// before cutoff, deletes the unprotected ones and moves the cursor past the
// last row it scanned. more is false once the scan reached the cutoff.
func trimLogsBatch(
	ctx context.Context, db *sql.DB, table string, pid int64, cutoff, errorLogCutoff time.Time,
	cur *logTrimCursor, limit int,
) (deleted int64, more bool, err error) {
	rows, err := db.QueryContext(ctx, logTrimSelectSQL(table, cur.set),
		pid, cutoff, cur.ingestedAt, cur.rowid, errorLogCutoff, limit)
	if err != nil {
		return 0, false, err
	}
	var ids []int64
	var scanned int
	var lastAt string
	var lastID int64
	for rows.Next() {
		var protected bool
		if err := rows.Scan(&lastID, &lastAt, &protected); err != nil {
			_ = rows.Close()
			return 0, false, err
		}
		scanned++
		if !protected {
			ids = append(ids, lastID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, false, err
	}
	if err := rows.Close(); err != nil {
		return 0, false, err
	}
	if scanned == 0 {
		return 0, false, nil
	}

	if len(ids) > 0 {
		payload, err := json.Marshal(ids)
		if err != nil {
			return 0, false, err
		}
		res, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE rowid IN (SELECT value FROM json_each(?))`, string(payload))
		if err != nil {
			return 0, false, err
		}
		deleted, _ = res.RowsAffected()
	}
	cur.set, cur.ingestedAt, cur.rowid = true, lastAt, lastID
	return deleted, scanned == limit, nil
}

// trimLogsFrom trims one project's rows of table, a window per low-priority
// write, resuming from the cursor the previous pass left.
func (r *Repository) trimLogsFrom(ctx context.Context, table string, pid int64, cutoff, errorLogCutoff time.Time) (int64, error) {
	f := TableFamily(table)
	label := "deleteLogsFrom:" + f.String()
	key := logTrimKey(table, pid)
	cur := r.logTrim.load(key, time.Now())
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var n int64
		var more bool
		err := r.submit(f, writescheduler.Low, label, func(db *sql.DB) error {
			var e error
			n, more, e = trimLogsBatch(ctx, db, table, pid, cutoff, errorLogCutoff, &cur, retentionDeleteBatch)
			return e
		})
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
