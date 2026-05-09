package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (r *Repository) InsertSpans(spans []Span) error {
	return r.InsertSpansContext(context.Background(), spans)
}

func (r *Repository) InsertSpansContext(ctx context.Context, spans []Span) error {
	if len(spans) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO spans
		(project_id, trace_id, span_id, parent_span_id, name, service, resource, kind, status, start_time_us, duration_us, attributes, events)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, s := range spans {
		var parentID *string
		if s.ParentSpanID != "" {
			parentID = &s.ParentSpanID
		}
		if _, err := stmt.ExecContext(ctx,
			s.ProjectID, s.TraceID, s.SpanID, parentID,
			s.Name, s.Service, s.Resource, s.Kind, s.Status,
			s.StartTimeUs, s.DurationUs, s.Attributes, s.Events,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) QuerySpans(f SpanFilter) ([]Span, error) {
	var where []string
	var args []any

	if f.ProjectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.TraceID != "" {
		where = append(where, "trace_id = ?")
		args = append(args, f.TraceID)
	}
	if f.Service != "" {
		where = append(where, "service = ?")
		args = append(args, f.Service)
	}
	if f.Operation != "" {
		where = append(where, "name = ?")
		args = append(args, f.Operation)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.MinDuration > 0 {
		where = append(where, "duration_us >= ?")
		args = append(args, f.MinDuration)
	}
	if !f.From.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, f.To)
	}

	q := "SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ingested_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	return r.scanSpans(q, args...)
}

func (r *Repository) GetTraceByID(traceID string) ([]Span, error) {
	return r.scanSpans(
		"SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans WHERE trace_id = ? ORDER BY start_time_us",
		traceID,
	)
}

func (r *Repository) GetSpansBySpanIDs(spanIDs []string) ([]Span, error) {
	if len(spanIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(spanIDs))
	args := make([]any, len(spanIDs))
	for i, id := range spanIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	return r.scanSpans(
		"SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans WHERE span_id IN ("+strings.Join(placeholders, ",")+")",
		args...,
	)
}

func (r *Repository) DeleteSpansByIDs(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := "DELETE FROM spans WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	res, err := r.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *Repository) DeleteSpansByMaxID(maxID int64) (int64, error) {
	res, err := r.db.Exec("DELETE FROM spans WHERE id <= ?", maxID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *Repository) DeleteBoringTraces(olderThan, newerThan time.Time, slowThresholdUS int64) (int64, error) {
	var total int64
	for {
		// Find trace_ids where ALL spans in the trace are boring (non-error, fast)
		// and at least one span falls in the retention window.
		res, err := r.db.Exec(`DELETE FROM spans WHERE trace_id IN (
			SELECT trace_id FROM spans
			WHERE ingested_at < ? AND ingested_at >= ?
			GROUP BY trace_id
			HAVING MAX(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) = 0
			AND MAX(duration_us) <= ?
			LIMIT 1000)`,
			olderThan, newerThan, slowThresholdUS)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n == 0 {
			break
		}
	}
	return total, nil
}

func (r *Repository) DeleteSpansOlderThan(cutoff time.Time) (int64, error) {
	res, err := r.db.Exec("DELETE FROM spans WHERE ingested_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *Repository) GetSpansForAggregation(cutoff time.Time, limit int) ([]Span, error) {
	if limit <= 0 {
		limit = 1000
	}
	return r.scanSpans(
		"SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans WHERE ingested_at <= ? ORDER BY ingested_at LIMIT ?",
		cutoff, limit,
	)
}

func (r *Repository) StreamSpans(f SpanFilter, fn func(Span) error) error {
	var where []string
	var args []any

	if f.ProjectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Service != "" {
		where = append(where, "service = ?")
		args = append(args, f.Service)
	}
	if f.Operation != "" {
		where = append(where, "name = ?")
		args = append(args, f.Operation)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.MinDuration > 0 {
		where = append(where, "duration_us >= ?")
		args = append(args, f.MinDuration)
	}
	if !f.From.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, f.To)
	}

	q := "SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ingested_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100000
	}
	q += fmt.Sprintf(" LIMIT %d", limit)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s Span
		if err := rows.Scan(
			&s.ID, &s.ProjectID, &s.TraceID, &s.SpanID, &s.ParentSpanID,
			&s.Name, &s.Service, &s.Resource, &s.Kind, &s.Status,
			&s.StartTimeUs, &s.DurationUs, &s.Attributes, &s.Events, &s.IngestedAt,
		); err != nil {
			return err
		}
		if err := fn(s); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *Repository) scanSpans(query string, args ...any) ([]Span, error) {
	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Span
	for rows.Next() {
		var s Span
		if err := rows.Scan(
			&s.ID, &s.ProjectID, &s.TraceID, &s.SpanID, &s.ParentSpanID,
			&s.Name, &s.Service, &s.Resource, &s.Kind, &s.Status,
			&s.StartTimeUs, &s.DurationUs, &s.Attributes, &s.Events, &s.IngestedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ServiceStats holds per-service metrics computed from raw spans.
type ServiceStats struct {
	Service    string
	Count      int64
	ErrorCount int64
	P50Us      int64
	P95Us      int64
	P99Us      int64
}

func (r *Repository) QueryServiceStatsFromSpans(projectID int64, from, to time.Time) ([]ServiceStats, error) {
	var where []string
	var args []any

	if projectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if !from.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, from)
	}
	if !to.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, to)
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	q := fmt.Sprintf(`SELECT service, COUNT(*) as cnt,
		SUM(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) as err_cnt
		FROM spans %s GROUP BY service`, whereClause)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ServiceStats
	for rows.Next() {
		var s ServiceStats
		if err := rows.Scan(&s.Service, &s.Count, &s.ErrorCount); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

type OperationStats struct {
	Operation  string
	Resource   string
	Kind       string
	Count      int64
	ErrorCount int64
	P50Us      int64
	P95Us      int64
	P99Us      int64
}

func (r *Repository) QueryOperationStatsFromSpans(projectID int64, service string, from, to time.Time) ([]OperationStats, error) {
	var where []string
	var args []any

	where = append(where, "service = ?")
	args = append(args, service)

	if projectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if !from.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, from)
	}
	if !to.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, to)
	}

	whereClause := strings.Join(where, " AND ")

	q := fmt.Sprintf(`SELECT name, resource, kind, COUNT(*) as cnt,
		SUM(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) as err_cnt
		FROM spans WHERE %s GROUP BY name, resource, kind`, whereClause)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []OperationStats
	for rows.Next() {
		var s OperationStats
		if err := rows.Scan(&s.Operation, &s.Resource, &s.Kind, &s.Count, &s.ErrorCount); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

type SpanBucket struct {
	Bucket     time.Time
	Count      int64
	ErrorCount int64
	P50Us      int64
	P95Us      int64
	P99Us      int64
}

func (r *Repository) QuerySpanTimeseries(projectID int64, service, operation string, from, to time.Time, intervalSec int64) ([]SpanBucket, error) {
	var where []string
	var args []any

	where = append(where, "service = ?", "name = ?")
	args = append(args, service, operation)

	if projectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if !from.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, from)
	}
	if !to.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, to)
	}

	whereClause := strings.Join(where, " AND ")
	bucketExpr := fmt.Sprintf("datetime((strftime('%%s', ingested_at) / %d) * %d, 'unixepoch')", intervalSec, intervalSec)

	q := fmt.Sprintf(`SELECT %s as bucket, COUNT(*) as cnt,
		SUM(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) as err_cnt
		FROM spans WHERE %s GROUP BY bucket ORDER BY bucket`, bucketExpr, whereClause)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SpanBucket
	for rows.Next() {
		var bucketStr string
		var sb SpanBucket
		if err := rows.Scan(&bucketStr, &sb.Count, &sb.ErrorCount); err != nil {
			return nil, err
		}
		sb.Bucket, _ = time.Parse("2006-01-02 15:04:05", bucketStr)
		result = append(result, sb)
	}
	return result, rows.Err()
}

type WebVitalRow struct {
	Page       string
	Metric     string
	ValueUs    int64
	Rating     string
	IngestedAt time.Time
}

func (r *Repository) QueryWebVitals(from, to time.Time) ([]WebVitalRow, error) {
	var where []string
	var args []any

	where = append(where, "name LIKE 'webvital.%'")
	if !from.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, from)
	}
	if !to.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, to)
	}

	q := `SELECT name, duration_us, attributes, ingested_at FROM spans WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ingested_at DESC LIMIT 10000`

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []WebVitalRow
	for rows.Next() {
		var name, attrsJSON string
		var durUs int64
		var ingestedAt time.Time
		if err := rows.Scan(&name, &durUs, &attrsJSON, &ingestedAt); err != nil {
			return nil, err
		}
		metric := strings.TrimPrefix(name, "webvital.")

		var attrs map[string]any
		_ = json.Unmarshal([]byte(attrsJSON), &attrs)

		page, _ := attrs["webvital.page"].(string)
		rating, _ := attrs["webvital.rating"].(string)
		if page == "" {
			page = "/"
		}

		result = append(result, WebVitalRow{
			Page:       page,
			Metric:     metric,
			ValueUs:    durUs,
			Rating:     rating,
			IngestedAt: ingestedAt,
		})
	}
	return result, rows.Err()
}
