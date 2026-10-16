package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// logTrimCursor marks how far one project's trim has scanned the
// (project_id, ingested_at) index. Rows before it were either deleted or are
// protected (pinned trace, recent error sample), so the next batch resumes after
// it instead of walking over the protected rows again.
type logTrimCursor struct {
	set        bool
	ingestedAt string // the stored text, so the comparison never depends on how the driver formats times
	rowid      int64
}

// logTrimSelectSQL picks one batch of deletable rows in index order. The cursor
// clause is added only after the first batch.
func logTrimSelectSQL(table string, resume bool) string {
	after := ""
	if resume {
		after = ` AND (l.ingested_at, l.rowid) > (?, ?)`
	}
	return `
		SELECT l.rowid, CAST(l.ingested_at AS TEXT) FROM ` + table + ` l
		WHERE l.project_id = ? AND l.ingested_at < ?` + after + `
		AND (l.trace_id IS NULL
		     OR (NOT EXISTS (
		             SELECT 1 FROM pinned_traces p
		             WHERE p.project_id = l.project_id
		               AND p.trace_id = l.trace_id
		         )
		         AND NOT EXISTS (
		             SELECT 1 FROM error_samples e
		             WHERE e.trace_id = l.trace_id
		               AND e.sampled_at > ?
		         )
		     )
		)
		ORDER BY l.ingested_at, l.rowid
		LIMIT ?`
}

// trimLogsBatch deletes up to limit deletable rows of one project that were
// ingested before cutoff and moves the cursor past the last row it picked.
func trimLogsBatch(
	ctx context.Context, db *sql.DB, table string, pid int64, cutoff, errorLogCutoff time.Time,
	cur *logTrimCursor, limit int,
) (int64, error) {
	args := []any{pid, cutoff}
	if cur.set {
		args = append(args, cur.ingestedAt, cur.rowid)
	}
	args = append(args, errorLogCutoff, limit)

	rows, err := db.QueryContext(ctx, logTrimSelectSQL(table, cur.set), args...)
	if err != nil {
		return 0, err
	}
	var ids []int64
	var lastAt string
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id, &lastAt); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	payload, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE rowid IN (SELECT value FROM json_each(?))`, string(payload))
	if err != nil {
		return 0, err
	}
	cur.set, cur.ingestedAt, cur.rowid = true, lastAt, ids[len(ids)-1]
	n, _ := res.RowsAffected()
	return n, nil
}
