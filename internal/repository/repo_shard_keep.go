package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// keepLogsSelectSQL reads one window of an attached logs shard. It is
// logTrimSelectSQL over expiring.logs, so it seeks the shard's
// (project_id, ingested_at) index and checks protection against main's
// pinned_traces and error_samples on the same connection.
func keepLogsSelectSQL(resume bool) string {
	return logWindowSelectSQL("expiring.logs AS l", "l", resume)
}

// keepLogsInsertSQL copies the columns kept_logs and the shard share. A shard
// can be a migration ahead of or behind kept_logs after a rollback, and a plain
// SELECT * would then fail and hold the shard back from expiring.
func keepLogsInsertSQL(ctx context.Context, conn *sql.Conn) (string, error) {
	kept, err := columnsOf(ctx, conn, "main", "kept_logs")
	if err != nil {
		return "", err
	}
	shard, err := columnsOf(ctx, conn, "expiring", "logs")
	if err != nil {
		return "", err
	}
	var shared []string
	for _, col := range kept {
		if slices.Contains(shard, col) {
			shared = append(shared, quoteColumn(col))
		}
	}
	cols := strings.Join(shared, ", ")
	return "INSERT OR IGNORE INTO main.kept_logs (" + cols + ") SELECT " + cols +
		" FROM expiring.logs WHERE rowid IN (SELECT value FROM json_each(?1))", nil
}

// keepLogsWindowRows caps the rows one keepLogs write slot scans, and so the
// rows it copies. Windows of retentionDeleteBatch (1000) rows took 0.5 to 4 s
// each when a day shard expired at midnight on 2026-10-29, and one took 13.5 s.
const keepLogsWindowRows = 250

// keepLogs copies the logs of error-sampled and pinned traces ingested before
// before from a logs shard into kept_logs. It runs on main's queue, where
// pinned_traces lives and error_samples is attached (or in main, in the
// single-file layout). kept_logs keeps the source ids, so a repeated copy
// inserts nothing.
//
// It copies one window of keepLogsWindowRows scanned rows per write slot,
// per project along the shard's (project_id, ingested_at) index, the way the
// log trim deletes. As one statement it held main's write slot for 37s on
// 2026-10-24, when the disk ladder cut the log window from 12h to 6h and the
// next trim copied six hours of a shard at once (#375).
func (r *Repository) keepLogs(ctx context.Context, row shardRow, before, errorLogCutoff time.Time) error {
	path := filepath.Join(r.shards.dir, row.file)
	var pids []int64
	_, err := r.onExpiringShard(ctx, path, func(conn *sql.Conn) (int64, error) {
		var err error
		pids, err = projectIDs(ctx, conn, "expiring.logs")
		return writescheduler.NoRows, err
	})
	if err != nil {
		return fmt.Errorf("keep logs of %s: %w", row.file, err)
	}
	for _, pid := range pids {
		if err := r.keepProjectLogs(ctx, path, pid, before, errorLogCutoff); err != nil {
			return fmt.Errorf("keep logs of %s: %w", row.file, err)
		}
	}
	return nil
}

// keepProjectLogs copies one project's protected rows, a window per write.
func (r *Repository) keepProjectLogs(ctx context.Context, path string, pid int64, before, errorLogCutoff time.Time) error {
	var cur logTrimCursor
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, more, err := r.keepLogsWindow(ctx, path, pid, before, errorLogCutoff, &cur)
		if err != nil || !more {
			return err
		}
		if r.deleteBatchYield > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(r.deleteBatchYield):
			}
		}
	}
}

// keepLogsWindow scans one window of a project's rows from cur on and copies
// its protected rows, in one write slot. It moves cur past the window and
// reports how many rows it copied and whether rows before the cutoff remain.
func (r *Repository) keepLogsWindow(
	ctx context.Context, path string, pid int64, before, errorLogCutoff time.Time, cur *logTrimCursor,
) (int64, bool, error) {
	var w logTrimWindow
	copied, err := r.onExpiringShard(ctx, path, func(conn *sql.Conn) (int64, error) {
		var err error
		w, err = scanLogWindow(ctx, conn, keepLogsSelectSQL(cur.set), pid, before.UTC(), errorLogCutoff.UTC(), *cur, keepLogsWindowRows)
		if err != nil || len(w.protected) == 0 {
			return 0, err
		}
		ids, err := json.Marshal(w.protected)
		if err != nil {
			return 0, err
		}
		insert, err := keepLogsInsertSQL(ctx, conn)
		if err != nil {
			return 0, err
		}
		res, err := conn.ExecContext(ctx, insert, string(ids))
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return n, nil
	})
	if err != nil {
		return copied, false, err
	}
	return copied, w.advance(cur, keepLogsWindowRows), nil
}

// onExpiringShard runs fn in one low-priority write slot on main, on a
// connection with the logs shard at path attached read-only as expiring. The
// job reports the rows fn returns as write.rows.
func (r *Repository) onExpiringShard(ctx context.Context, path string, fn func(conn *sql.Conn) (int64, error)) (int64, error) {
	return r.submitRows(FamilyCore, writescheduler.Low, "keepLogs", func(db *sql.DB) (int64, error) {
		conn, err := db.Conn(ctx)
		if err != nil {
			return 0, err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS expiring`, "file:"+path+"?mode=ro"); err != nil {
			return 0, fmt.Errorf("attach %s: %w", filepath.Base(path), err)
		}
		defer func() { _, _ = conn.ExecContext(context.Background(), `DETACH DATABASE expiring`) }()
		return fn(conn)
	})
}
