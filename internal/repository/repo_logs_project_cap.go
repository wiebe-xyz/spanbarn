package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// projectLogCapChunk is how many rows one write job of
// DeleteProjectLogsOlderThan deletes. A job of up to 1000 rows held the core
// writer with its row count unreported (#431). The jobs now
// report write.rows, so this size can be checked against what they show.
const projectLogCapChunk = 250

// projectLogVictimsSQL reads the oldest rows of one project past the cutoff that
// no pinned trace keeps. The last column is read only to load the trace index
// page the DELETE changes into the page cache, outside the write slot.
func projectLogVictimsSQL(table string) string {
	return fmt.Sprintf(`SELECT l.rowid,
		(SELECT 1 FROM %[1]s k WHERE k.trace_id = l.trace_id LIMIT 1)
		FROM %[1]s l
		WHERE l.project_id = ?1 AND l.ingested_at < ?2
		AND (l.trace_id IS NULL OR NOT EXISTS (
			SELECT 1 FROM pinned_traces p
			WHERE p.project_id = l.project_id AND p.trace_id = l.trace_id))
		LIMIT ?3`, table)
}

// deleteProjectLogRowsSQL deletes the listed rows and checks each one again: the
// list was read outside the write slot, so a trace can have been pinned since.
func deleteProjectLogRowsSQL(table string) string {
	return fmt.Sprintf(`DELETE FROM %[1]s
		WHERE rowid IN (SELECT value FROM json_each(?1))
		AND project_id = ?2 AND ingested_at < ?3
		AND (trace_id IS NULL OR NOT EXISTS (
			SELECT 1 FROM pinned_traces p
			WHERE p.project_id = %[1]s.project_id AND p.trace_id = %[1]s.trace_id))`, table)
}

// deleteProjectLogsFromTable deletes one project's rows of a table in main past
// the cutoff, projectLogCapChunk per write job, with each chunk read off the
// write slot.
func (r *Repository) deleteProjectLogsFromTable(ctx context.Context, table string, projectID int64, cutoff time.Time) (int64, error) {
	f := TableFamily(table)
	label := "DeleteProjectLogsOlderThan:" + f.String()
	query := deleteProjectLogRowsSQL(table)
	var total int64
	for first := true; ; first = false {
		if !first && !r.yieldBetweenBatches(ctx) {
			return total, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
		ids, err := r.projectLogVictims(ctx, table, projectID, cutoff)
		if err != nil || len(ids) == 0 {
			return total, err
		}
		n, err := r.submitRows(f, writescheduler.Low, label, func(db *sql.DB) (int64, error) {
			return deleteRowsByID(ctx, db, query, ids, projectID, cutoff)
		})
		total += n
		if err != nil || n == 0 || len(ids) < projectLogCapChunk {
			return total, err
		}
	}
}

// projectLogVictims reads up to projectLogCapChunk row ids on the read pool when
// there is one.
func (r *Repository) projectLogVictims(ctx context.Context, table string, projectID int64, cutoff time.Time) ([]int64, error) {
	var q rowQuerier = r.db
	if r.logTrimReads != nil {
		q = r.logTrimReads
	}
	rows, err := q.QueryContext(ctx, projectLogVictimsSQL(table), projectID, cutoff, projectLogCapChunk)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var warm sql.NullInt64
		if err := rows.Scan(&id, &warm); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, rows.Close()
}
