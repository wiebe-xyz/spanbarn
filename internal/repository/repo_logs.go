package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// LogRow is the repository-layer representation of a stored log entry.
type LogRow struct {
	ID                   int64
	ProjectID            int64
	TraceID              string
	SpanID               string
	SeverityNumber       int32
	SeverityText         string
	TimeUnixNano         int64
	ObservedTimeUnixNano int64
	Body                 string
	Attributes           string
	IngestedAt           time.Time
}

// LogFilter scopes log queries.
type LogFilter struct {
	ProjectID   int64
	TraceID     string
	SpanID      string
	MinSeverity int32  // inclusive lower bound on severity_number; 0 = no filter
	Service     string // matches JSON_EXTRACT(attributes,'$.service.name')
	Search      string // body LIKE %search%
	// Attributes keeps logs whose attribute equals the value, for each pair.
	Attributes map[string]string
	// Ascending returns the oldest log first; the default is newest first.
	Ascending bool
	From      time.Time
	To        time.Time
	Limit     int
	Offset    int
}

// PinnedTrace is a user-saved trace reference that exempts its logs from normal deletion.
type PinnedTrace struct {
	ProjectID int64
	TraceID   string
	Label     string
	PinnedAt  time.Time
}

// InsertLogs persists a batch of log records in a single transaction.
func (r *Repository) InsertLogs(ctx context.Context, recs []model.LogRecord) error {
	if len(recs) == 0 {
		return nil
	}
	// Writing telemetry must not emit telemetry. See WithoutSpanTracing.
	ctx = WithoutSpanTracing(ctx)
	return r.execIngest(FamilyLogs, int64(len(recs)), func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		stmt, err := tx.PrepareContext(ctx, `INSERT INTO logs
			(project_id, trace_id, span_id, severity_number, severity_text,
			 time_unix_nano, observed_time_unix_nano, body, attributes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, rec := range recs {
			var traceID, spanID *string
			if rec.TraceID != "" {
				traceID = &rec.TraceID
			}
			if rec.SpanID != "" {
				spanID = &rec.SpanID
			}
			attrs := string(rec.Attributes)
			if attrs == "" || attrs == "null" {
				attrs = "{}"
			}
			if _, err := stmt.ExecContext(ctx,
				rec.ProjectID, traceID, spanID,
				rec.SeverityNumber, rec.SeverityText,
				int64(rec.TimeUnixNano), int64(rec.ObservedTimeUnixNano),
				rec.Body, attrs,
			); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// QueryLogs returns log rows and total count matching f.
func (r *Repository) QueryLogs(ctx context.Context, f LogFilter) ([]LogRow, int, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	where := []string{"project_id = ?", "ingested_at >= ?", "ingested_at <= ?"}
	args := []any{f.ProjectID, f.From, f.To}

	if f.TraceID != "" {
		where = append(where, "trace_id = ?")
		args = append(args, f.TraceID)
	}
	if f.SpanID != "" {
		where = append(where, "span_id = ?")
		args = append(args, f.SpanID)
	}
	if f.MinSeverity > 0 {
		where = append(where, "severity_number >= ?")
		args = append(args, f.MinSeverity)
	}
	if f.Service != "" {
		where = append(where, `JSON_EXTRACT(attributes, '$."service.name"') = ?`)
		args = append(args, f.Service)
	}
	if f.Search != "" {
		where = append(where, "body LIKE ?")
		args = append(args, "%"+f.Search+"%")
	}

	where, args, err := appendAttrFilters(where, args, f.Attributes)
	if err != nil {
		return nil, 0, err
	}
	cond := strings.Join(where, " AND ")
	order := "DESC"
	if f.Ascending {
		order = "ASC"
	}

	var total int
	db := r.reader(FamilyLogs)
	if err := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM logs WHERE %s`, cond),
		args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	pageArgs := append(args, limit, f.Offset)
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		`SELECT id, project_id, COALESCE(trace_id,''), COALESCE(span_id,''),
		        severity_number, severity_text,
		        time_unix_nano, observed_time_unix_nano, body, attributes, ingested_at
		 FROM logs WHERE %s ORDER BY time_unix_nano %s LIMIT ? OFFSET ?`, cond, order),
		pageArgs...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []LogRow
	for rows.Next() {
		var row LogRow
		if err := rows.Scan(
			&row.ID, &row.ProjectID, &row.TraceID, &row.SpanID,
			&row.SeverityNumber, &row.SeverityText,
			&row.TimeUnixNano, &row.ObservedTimeUnixNano,
			&row.Body, &row.Attributes, &row.IngestedAt,
		); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}

// LogHistogramBucket is one time-bucket in a log volume histogram.
type LogHistogramBucket struct {
	Ts    time.Time `json:"ts"`
	Count int       `json:"count"`
}

// LogHistogram returns per-bucket log counts for the given filter.
// bucketSecs controls the width of each bucket in seconds.
func (r *Repository) LogHistogram(ctx context.Context, f LogFilter, bucketSecs int) ([]LogHistogramBucket, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	where := []string{"project_id = ?", "ingested_at >= ?", "ingested_at <= ?"}
	args := []any{f.ProjectID, f.From, f.To}

	if f.TraceID != "" {
		where = append(where, "trace_id = ?")
		args = append(args, f.TraceID)
	}
	if f.MinSeverity > 0 {
		where = append(where, "severity_number >= ?")
		args = append(args, f.MinSeverity)
	}
	if f.Service != "" {
		where = append(where, `JSON_EXTRACT(attributes, '$."service.name"') = ?`)
		args = append(args, f.Service)
	}
	if f.Search != "" {
		where = append(where, "body LIKE ?")
		args = append(args, "%"+f.Search+"%")
	}

	cond := strings.Join(where, " AND ")
	// Prepend bucketSecs twice: once for the division, once for the multiply.
	qargs := append([]any{bucketSecs, bucketSecs}, args...)

	rows, err := r.reader(FamilyLogs).QueryContext(ctx, fmt.Sprintf(`
		SELECT CAST(strftime('%%s', ingested_at) / ? AS INTEGER) * ? AS bucket_ts, COUNT(*)
		FROM logs WHERE %s
		GROUP BY bucket_ts ORDER BY bucket_ts`, cond), qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []LogHistogramBucket
	for rows.Next() {
		var epochSecs int64
		var cnt int
		if err := rows.Scan(&epochSecs, &cnt); err != nil {
			return nil, err
		}
		result = append(result, LogHistogramBucket{
			Ts:    time.Unix(epochSecs, 0).UTC(),
			Count: cnt,
		})
	}
	return result, rows.Err()
}

// DeleteLogsOlderThan removes log records ingested before cutoff, skipping logs
// whose trace_id is pinned or appears in recently-sampled error_samples.
func (r *Repository) DeleteLogsOlderThan(ctx context.Context, cutoff, errorLogCutoff time.Time) (int64, error) {
	// Per project so each batch seeks idx_logs_project_ingested instead of
	// full-scanning the logs table on a global `WHERE ingested_at < ?` (logs has
	// no standalone ingested_at index).
	//
	// The exemptions use NOT EXISTS (indexed seek per candidate row) rather than
	// `trace_id NOT IN (SELECT ... FROM error_samples ...)`. The NOT IN form
	// materialised the whole error_samples table (270k+ rows, a full scan with a
	// per-row sampled_at fetch) on EVERY batch of EVERY project — a single batch
	// took 90s+ and held the one write slot the whole time, wedging the writer.
	// NOT EXISTS seeks idx_error_samples_trace by the candidate row's trace_id, so
	// the cost scales with the small logs set, not the large error_samples table.
	// It is also NULL-safe (NOT IN deletes nothing if the subquery yields a NULL).
	//
	// kept_logs (the error-trace and pinned logs copied out of expired logs
	// shards) expires under the same rule.
	n, err := r.deleteLogsFrom(ctx, "logs", cutoff, errorLogCutoff)
	if err != nil {
		return n, err
	}
	kept, err := r.deleteLogsFrom(ctx, "kept_logs", cutoff, errorLogCutoff)
	return n + kept, err
}

func (r *Repository) deleteLogsFrom(ctx context.Context, table string, cutoff, errorLogCutoff time.Time) (int64, error) {
	pids, err := r.distinctProjectIDs(ctx, table)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, pid := range pids {
		n, err := r.trimLogsFrom(ctx, table, pid, cutoff, errorLogCutoff)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// DeleteProjectLogsOlderThan removes one project's log records ingested before
// cutoff, error-trace logs included, from the live and kept tables. It backs a
// per-project cap for projects whose logs hold content that should not outlive
// it. Logs of pinned traces are still kept. With sharded logs the rows live in
// the shard files, so those are trimmed too; main's own tables may be gone.
func (r *Repository) DeleteProjectLogsOlderThan(ctx context.Context, projectID int64, cutoff time.Time) (int64, error) {
	var total int64
	for _, table := range []string{"logs", "kept_logs"} {
		// Main drops a sharded family's table once it is empty.
		if has, err := r.readsHaveTable(ctx, table); err != nil {
			return total, err
		} else if !has {
			continue
		}
		n, err := r.batchedDelete(ctx, TableFamily(table), func(db *sql.DB) (int64, error) {
			res, e := db.ExecContext(ctx, `
				DELETE FROM `+table+` WHERE rowid IN (
				    SELECT rowid FROM `+table+` l
				    WHERE project_id = ? AND ingested_at < ?
				    AND (trace_id IS NULL OR NOT EXISTS (
				            SELECT 1 FROM pinned_traces p
				            WHERE p.project_id = l.project_id AND p.trace_id = l.trace_id))
				    LIMIT ?)`,
				projectID, cutoff, retentionDeleteBatch,
			)
			if e != nil {
				return 0, e
			}
			n, _ := res.RowsAffected()
			return n, nil
		})
		total += n
		if err != nil {
			return total, err
		}
	}
	n, err := r.deleteProjectLogsFromShards(ctx, projectID, cutoff)
	return total + n, err
}

// deleteProjectLogsFromShards removes one project's logs older than cutoff from
// every logs shard that can hold them, sparing logs of pinned traces. Pins live
// in main, so they are read once and handed to each shard as a JSON list.
func (r *Repository) deleteProjectLogsFromShards(ctx context.Context, projectID int64, cutoff time.Time) (int64, error) {
	m := r.shards
	if m == nil || !m.Sharded(FamilyLogs) {
		return 0, nil
	}
	rows, err := m.rows(ctx)
	if err != nil {
		return 0, err
	}
	pinned, err := r.pinnedTraceIDsJSON(ctx, projectID)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, row := range rows {
		// A shard that starts after the cutoff holds nothing older than it.
		if row.family != FamilyLogs || row.start.After(cutoff) {
			continue
		}
		for {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			var n int64
			if err := m.onShard(row.family, row.start, "projectLogCap", func(db *sql.DB) error {
				res, e := db.ExecContext(ctx, `
					DELETE FROM logs WHERE rowid IN (
					    SELECT rowid FROM logs
					    WHERE project_id = ? AND ingested_at < ?
					    AND (trace_id IS NULL OR trace_id NOT IN (SELECT value FROM json_each(?)))
					    LIMIT ?)`,
					projectID, cutoff.UTC(), pinned, retentionDeleteBatch)
				if e != nil {
					return e
				}
				n, _ = res.RowsAffected()
				return nil
			}); err != nil {
				return total, err
			}
			total += n
			if n < retentionDeleteBatch {
				break
			}
		}
	}
	return total, nil
}

// pinnedTraceIDsJSON returns the project's pinned trace ids as a JSON array.
func (r *Repository) pinnedTraceIDsJSON(ctx context.Context, projectID int64) (string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT trace_id FROM pinned_traces WHERE project_id = ?`, projectID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	b, err := json.Marshal(ids)
	return string(b), err
}

// --- Pinned Traces ---

func (r *Repository) PinTrace(ctx context.Context, projectID int64, traceID, label string) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO pinned_traces (project_id, trace_id, label) VALUES (?, ?, ?)
			 ON CONFLICT(project_id, trace_id) DO UPDATE SET label = excluded.label`,
			projectID, traceID, label,
		)
		return err
	})
}

func (r *Repository) UnpinTrace(ctx context.Context, projectID int64, traceID string) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		_, err := db.ExecContext(ctx,
			`DELETE FROM pinned_traces WHERE project_id = ? AND trace_id = ?`,
			projectID, traceID,
		)
		return err
	})
}

func (r *Repository) ListPinnedTraces(ctx context.Context, projectID int64) ([]PinnedTrace, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT project_id, trace_id, label, pinned_at FROM pinned_traces
		 WHERE project_id = ? ORDER BY pinned_at DESC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PinnedTrace
	for rows.Next() {
		var p PinnedTrace
		if err := rows.Scan(&p.ProjectID, &p.TraceID, &p.Label, &p.PinnedAt); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (r *Repository) IsTracePinned(ctx context.Context, projectID int64, traceID string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pinned_traces WHERE project_id = ? AND trace_id = ?`,
		projectID, traceID,
	).Scan(&n)
	return n > 0, err
}
