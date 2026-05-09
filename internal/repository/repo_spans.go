package repository

import (
	"context"
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

func (r *Repository) DeleteBoringSpans(olderThan, newerThan time.Time, slowThresholdUS int64) (int64, error) {
	var total int64
	for {
		res, err := r.db.Exec(`DELETE FROM spans WHERE id IN (
			SELECT id FROM spans
			WHERE ingested_at < ? AND ingested_at >= ?
			AND status NOT IN ('error','ERROR','Error')
			AND duration_us <= ?
			LIMIT 5000)`,
			olderThan, newerThan, slowThresholdUS)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < 5000 {
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

	ctx, cancel := r.queryContext()
	defer cancel()

	// Step 1: get counts per service (fast GROUP BY).
	countQ := fmt.Sprintf(`SELECT service, COUNT(*) as cnt,
		SUM(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) as err_cnt
		FROM spans %s GROUP BY service`, whereClause)
	countRows, err := r.db.QueryContext(ctx, countQ, args...)
	if err != nil {
		return nil, err
	}
	type svcCount struct {
		count, errors          int64
		p50Idx, p95Idx, p99Idx int64
	}
	counts := make(map[string]*svcCount)
	var order []string
	for countRows.Next() {
		var svc string
		var sc svcCount
		if err := countRows.Scan(&svc, &sc.count, &sc.errors); err != nil {
			countRows.Close()
			return nil, err
		}
		sc.p50Idx = max(1, sc.count*50/100)
		sc.p95Idx = max(1, sc.count*95/100)
		sc.p99Idx = max(1, sc.count*99/100)
		counts[svc] = &sc
		order = append(order, svc)
	}
	countRows.Close()

	// Step 2: stream sorted rows to pick percentile values at exact positions.
	streamQ := fmt.Sprintf(`SELECT service, duration_us FROM spans %s ORDER BY service, duration_us`, whereClause)
	rows, err := r.db.QueryContext(ctx, streamQ, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statsMap := make(map[string]*ServiceStats, len(counts))
	for svc, sc := range counts {
		statsMap[svc] = &ServiceStats{Service: svc, Count: sc.count, ErrorCount: sc.errors}
	}

	var curSvc string
	var pos int64
	for rows.Next() {
		var svc string
		var dur int64
		if err := rows.Scan(&svc, &dur); err != nil {
			return nil, err
		}
		if svc != curSvc {
			curSvc = svc
			pos = 0
		}
		pos++
		sc := counts[svc]
		if sc == nil {
			continue
		}
		st := statsMap[svc]
		if pos == sc.p50Idx {
			st.P50Us = dur
		}
		if pos == sc.p95Idx {
			st.P95Us = dur
		}
		if pos == sc.p99Idx {
			st.P99Us = dur
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]ServiceStats, 0, len(order))
	for _, svc := range order {
		result = append(result, *statsMap[svc])
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

	ctx, cancel := r.queryContext()
	defer cancel()

	type opKey struct{ name, resource, kind string }
	type opCount struct {
		count, errors          int64
		p50Idx, p95Idx, p99Idx int64
	}

	countQ := fmt.Sprintf(`SELECT name, resource, kind, COUNT(*) as cnt,
		SUM(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) as err_cnt
		FROM spans WHERE %s GROUP BY name, resource, kind`, whereClause)
	countRows, err := r.db.QueryContext(ctx, countQ, args...)
	if err != nil {
		return nil, err
	}
	counts := make(map[opKey]*opCount)
	var order []opKey
	for countRows.Next() {
		var k opKey
		var oc opCount
		if err := countRows.Scan(&k.name, &k.resource, &k.kind, &oc.count, &oc.errors); err != nil {
			countRows.Close()
			return nil, err
		}
		oc.p50Idx = max(1, oc.count*50/100)
		oc.p95Idx = max(1, oc.count*95/100)
		oc.p99Idx = max(1, oc.count*99/100)
		counts[k] = &oc
		order = append(order, k)
	}
	countRows.Close()

	statsMap := make(map[opKey]*OperationStats, len(counts))
	for k, oc := range counts {
		statsMap[k] = &OperationStats{
			Operation: k.name, Resource: k.resource, Kind: k.kind,
			Count: oc.count, ErrorCount: oc.errors,
		}
	}

	streamQ := fmt.Sprintf(`SELECT name, resource, kind, duration_us
		FROM spans WHERE %s ORDER BY name, resource, kind, duration_us`, whereClause)
	rows, err := r.db.QueryContext(ctx, streamQ, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var curKey opKey
	var pos int64
	for rows.Next() {
		var k opKey
		var dur int64
		if err := rows.Scan(&k.name, &k.resource, &k.kind, &dur); err != nil {
			return nil, err
		}
		if k != curKey {
			curKey = k
			pos = 0
		}
		pos++
		oc := counts[k]
		if oc == nil {
			continue
		}
		st := statsMap[k]
		if pos == oc.p50Idx {
			st.P50Us = dur
		}
		if pos == oc.p95Idx {
			st.P95Us = dur
		}
		if pos == oc.p99Idx {
			st.P99Us = dur
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]OperationStats, 0, len(order))
	for _, k := range order {
		result = append(result, *statsMap[k])
	}
	return result, nil
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

	ctx, cancel := r.queryContext()
	defer cancel()

	// Step 1: counts per bucket.
	countQ := fmt.Sprintf(`SELECT %s as bucket, COUNT(*) as cnt,
		SUM(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) as err_cnt
		FROM spans WHERE %s GROUP BY bucket ORDER BY bucket`, bucketExpr, whereClause)
	countRows, err := r.db.QueryContext(ctx, countQ, args...)
	if err != nil {
		return nil, err
	}
	type bucketCount struct {
		count, errors          int64
		p50Idx, p95Idx, p99Idx int64
	}
	counts := make(map[string]*bucketCount)
	var order []string
	for countRows.Next() {
		var bucket string
		var bc bucketCount
		if err := countRows.Scan(&bucket, &bc.count, &bc.errors); err != nil {
			countRows.Close()
			return nil, err
		}
		bc.p50Idx = max(1, bc.count*50/100)
		bc.p95Idx = max(1, bc.count*95/100)
		bc.p99Idx = max(1, bc.count*99/100)
		counts[bucket] = &bc
		order = append(order, bucket)
	}
	countRows.Close()

	statsMap := make(map[string]*SpanBucket, len(counts))
	for bucket, bc := range counts {
		t, _ := time.Parse("2006-01-02 15:04:05", bucket)
		statsMap[bucket] = &SpanBucket{Bucket: t, Count: bc.count, ErrorCount: bc.errors}
	}

	// Step 2: stream sorted rows for percentiles.
	streamQ := fmt.Sprintf(`SELECT %s as bucket, duration_us
		FROM spans WHERE %s ORDER BY bucket, duration_us`, bucketExpr, whereClause)
	rows, err := r.db.QueryContext(ctx, streamQ, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var curBucket string
	var pos int64
	for rows.Next() {
		var bucket string
		var dur int64
		if err := rows.Scan(&bucket, &dur); err != nil {
			return nil, err
		}
		if bucket != curBucket {
			curBucket = bucket
			pos = 0
		}
		pos++
		bc := counts[bucket]
		if bc == nil {
			continue
		}
		sb := statsMap[bucket]
		if pos == bc.p50Idx {
			sb.P50Us = dur
		}
		if pos == bc.p95Idx {
			sb.P95Us = dur
		}
		if pos == bc.p99Idx {
			sb.P99Us = dur
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]SpanBucket, 0, len(order))
	for _, bucket := range order {
		result = append(result, *statsMap[bucket])
	}
	return result, nil
}
