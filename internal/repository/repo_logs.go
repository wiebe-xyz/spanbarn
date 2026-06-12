package repository

import (
	"context"
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
	ProjectID      int64
	TraceID        string
	SpanID         string
	MinSeverity    int32  // inclusive lower bound on severity_number; 0 = no filter
	Service        string // matches JSON_EXTRACT(attributes,'$.service.name')
	Search         string // body LIKE %search%
	From           time.Time
	To             time.Time
	Limit          int
	Offset         int
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
	tx, err := r.db.BeginTx(ctx, nil)
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

	cond := strings.Join(where, " AND ")

	var total int
	if err := r.db.QueryRowContext(ctx,
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
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT id, project_id, COALESCE(trace_id,''), COALESCE(span_id,''),
		        severity_number, severity_text,
		        time_unix_nano, observed_time_unix_nano, body, attributes, ingested_at
		 FROM logs WHERE %s ORDER BY time_unix_nano DESC LIMIT ? OFFSET ?`, cond),
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

// DeleteLogsOlderThan removes log records ingested before cutoff, skipping logs
// whose trace_id is pinned or appears in recently-sampled error_samples.
func (r *Repository) DeleteLogsOlderThan(ctx context.Context, cutoff, errorLogCutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM logs
		WHERE ingested_at < ?
		AND (trace_id IS NULL
		     OR (trace_id NOT IN (
		             SELECT trace_id FROM pinned_traces
		             WHERE project_id = logs.project_id
		         )
		         AND trace_id NOT IN (
		             SELECT DISTINCT trace_id FROM error_samples
		             WHERE sampled_at > ?
		         )
		     )
		)`,
		cutoff, errorLogCutoff,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- Pinned Traces ---

func (r *Repository) PinTrace(ctx context.Context, projectID int64, traceID, label string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO pinned_traces (project_id, trace_id, label) VALUES (?, ?, ?)
		 ON CONFLICT(project_id, trace_id) DO UPDATE SET label = excluded.label`,
		projectID, traceID, label,
	)
	return err
}

func (r *Repository) UnpinTrace(ctx context.Context, projectID int64, traceID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM pinned_traces WHERE project_id = ? AND trace_id = ?`,
		projectID, traceID,
	)
	return err
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

