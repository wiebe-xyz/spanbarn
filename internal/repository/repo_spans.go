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

// GetBoringTraceSpans returns spans from traces that are entirely boring
// (no error spans, no slow spans) with ingested_at older than cutoff.
// Used to aggregate-then-delete boring traces on a shorter TTL than error traces.
func (r *Repository) GetBoringTraceSpans(cutoff time.Time, slowThresholdUS int64, limit int) ([]Span, error) {
	if limit <= 0 {
		limit = 1000
	}
	return r.scanSpans(`
		SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at
		FROM spans
		WHERE trace_id IN (
			SELECT trace_id FROM spans
			WHERE ingested_at < ?
			GROUP BY trace_id
			HAVING MAX(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) = 0
			   AND MAX(duration_us) <= ?
			LIMIT 100
		)
		ORDER BY id
		LIMIT ?`,
		cutoff, slowThresholdUS, limit,
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

	q := fmt.Sprintf(`SELECT service, duration_us, status FROM spans %s ORDER BY service, duration_us`, whereClause)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type svcBucket struct {
		durations  []int64
		errorCount int64
	}
	byService := make(map[string]*svcBucket)
	for rows.Next() {
		var service, status string
		var durationUs int64
		if err := rows.Scan(&service, &durationUs, &status); err != nil {
			return nil, err
		}
		b, ok := byService[service]
		if !ok {
			b = &svcBucket{}
			byService[service] = b
		}
		b.durations = append(b.durations, durationUs)
		if status == "error" || status == "ERROR" || status == "Error" {
			b.errorCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]ServiceStats, 0, len(byService))
	for svc, b := range byService {
		result = append(result, ServiceStats{
			Service:    svc,
			Count:      int64(len(b.durations)),
			ErrorCount: b.errorCount,
			P50Us:      percentileFromSorted(b.durations, 50),
			P95Us:      percentileFromSorted(b.durations, 95),
			P99Us:      percentileFromSorted(b.durations, 99),
		})
	}
	return result, nil
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

	q := fmt.Sprintf(`SELECT name, resource, kind, duration_us, status FROM spans WHERE %s ORDER BY name, resource, kind, duration_us`, whereClause)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type opKey struct{ operation, resource, kind string }
	type opBucket struct {
		durations  []int64
		errorCount int64
	}
	byOp := make(map[opKey]*opBucket)
	for rows.Next() {
		var name, resource, kind, status string
		var durationUs int64
		if err := rows.Scan(&name, &resource, &kind, &durationUs, &status); err != nil {
			return nil, err
		}
		k := opKey{name, resource, kind}
		b, ok := byOp[k]
		if !ok {
			b = &opBucket{}
			byOp[k] = b
		}
		b.durations = append(b.durations, durationUs)
		if status == "error" || status == "ERROR" || status == "Error" {
			b.errorCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]OperationStats, 0, len(byOp))
	for k, b := range byOp {
		result = append(result, OperationStats{
			Operation:  k.operation,
			Resource:   k.resource,
			Kind:       k.kind,
			Count:      int64(len(b.durations)),
			ErrorCount: b.errorCount,
			P50Us:      percentileFromSorted(b.durations, 50),
			P95Us:      percentileFromSorted(b.durations, 95),
			P99Us:      percentileFromSorted(b.durations, 99),
		})
	}
	return result, nil
}

// percentileFromSorted computes a percentile from an already-sorted slice.
func percentileFromSorted(sorted []int64, pct float64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(pct/100.0*float64(n)+0.5) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
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

	q := fmt.Sprintf(`SELECT %s as bucket, duration_us, status FROM spans WHERE %s ORDER BY bucket, duration_us`, bucketExpr, whereClause)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rawBucket struct {
		durations  []int64
		errorCount int64
	}
	byBucket := make(map[string]*rawBucket)
	var bucketOrder []string
	for rows.Next() {
		var bucketStr, status string
		var durationUs int64
		if err := rows.Scan(&bucketStr, &durationUs, &status); err != nil {
			return nil, err
		}
		b, ok := byBucket[bucketStr]
		if !ok {
			b = &rawBucket{}
			byBucket[bucketStr] = b
			bucketOrder = append(bucketOrder, bucketStr)
		}
		b.durations = append(b.durations, durationUs)
		if status == "error" || status == "ERROR" || status == "Error" {
			b.errorCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]SpanBucket, 0, len(byBucket))
	for _, bucketStr := range bucketOrder {
		b := byBucket[bucketStr]
		t, _ := time.Parse("2006-01-02 15:04:05", bucketStr)
		result = append(result, SpanBucket{
			Bucket:     t,
			Count:      int64(len(b.durations)),
			ErrorCount: b.errorCount,
			P50Us:      percentileFromSorted(b.durations, 50),
			P95Us:      percentileFromSorted(b.durations, 95),
			P99Us:      percentileFromSorted(b.durations, 99),
		})
	}
	return result, nil
}

type WebVitalRow struct {
	Service    string
	Page       string
	Metric     string
	ValueUs    int64
	Rating     string
	IngestedAt time.Time
}

func (r *Repository) QueryWebVitals(service string, from, to time.Time) ([]WebVitalRow, error) {
	var where []string
	var args []any

	where = append(where, "name LIKE 'webvital.%'")
	if service != "" {
		where = append(where, "service = ?")
		args = append(args, service)
	}
	if !from.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, from)
	}
	if !to.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, to)
	}

	q := `SELECT service, name, duration_us, attributes, ingested_at FROM spans WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ingested_at DESC LIMIT 10000`

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []WebVitalRow
	for rows.Next() {
		var svc, name, attrsJSON string
		var durUs int64
		var ingestedAt time.Time
		if err := rows.Scan(&svc, &name, &durUs, &attrsJSON, &ingestedAt); err != nil {
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
			Service:    svc,
			Page:       page,
			Metric:     metric,
			ValueUs:    durUs,
			Rating:     rating,
			IngestedAt: ingestedAt,
		})
	}
	return result, rows.Err()
}

// WebVitalBucket holds bucketed web vital metrics for timeseries display.
type WebVitalBucket struct {
	Bucket  time.Time
	Page    string
	Metric  string
	P50Us   int64
	P95Us   int64
	Samples int64
	Good    int64
	NI      int64
	Poor    int64
}

// QueryWebVitalsTimeseries returns time-bucketed web vital data for a specific page and metric.
func (r *Repository) QueryWebVitalsTimeseries(service, page, metric string, from, to time.Time, intervalSec int64) ([]WebVitalBucket, error) {
	var where []string
	var args []any

	where = append(where, "name = ?")
	args = append(args, "webvital."+metric)

	if service != "" {
		where = append(where, "service = ?")
		args = append(args, service)
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

	q := fmt.Sprintf(`SELECT %s as bucket, duration_us, attributes FROM spans WHERE %s ORDER BY bucket, duration_us`, bucketExpr, whereClause)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rawBucket struct {
		values             []int64
		good, ni, poor     int64
	}
	byBucket := make(map[string]*rawBucket)
	var bucketOrder []string

	for rows.Next() {
		var bucketStr, attrsJSON string
		var durationUs int64
		if err := rows.Scan(&bucketStr, &durationUs, &attrsJSON); err != nil {
			return nil, err
		}

		var attrs map[string]any
		_ = json.Unmarshal([]byte(attrsJSON), &attrs)

		spanPage, _ := attrs["webvital.page"].(string)
		if spanPage == "" {
			spanPage = "/"
		}
		if page != "" && spanPage != page {
			continue
		}

		rating, _ := attrs["webvital.rating"].(string)

		b, ok := byBucket[bucketStr]
		if !ok {
			b = &rawBucket{}
			byBucket[bucketStr] = b
			bucketOrder = append(bucketOrder, bucketStr)
		}
		b.values = append(b.values, durationUs)
		switch rating {
		case "good":
			b.good++
		case "needs-improvement":
			b.ni++
		default:
			b.poor++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]WebVitalBucket, 0, len(byBucket))
	for _, bucketStr := range bucketOrder {
		b := byBucket[bucketStr]
		t, _ := time.Parse("2006-01-02 15:04:05", bucketStr)
		result = append(result, WebVitalBucket{
			Bucket:  t,
			Page:    page,
			Metric:  metric,
			P50Us:   percentileFromSorted(b.values, 50),
			P95Us:   percentileFromSorted(b.values, 95),
			Samples: int64(len(b.values)),
			Good:    b.good,
			NI:      b.ni,
			Poor:    b.poor,
		})
	}
	return result, nil
}
