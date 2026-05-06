package repository

import (
	"fmt"
	"strings"
	"time"
)

func (r *Repository) InsertSpans(spans []Span) error {
	if len(spans) == 0 {
		return nil
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO spans
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
		if _, err := stmt.Exec(
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

func (r *Repository) scanSpans(query string, args ...any) ([]Span, error) {
	rows, err := r.db.Query(query, args...)
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
	Durations  []int64
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

	q := "SELECT service, COUNT(*) as cnt, SUM(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) as err_cnt FROM spans"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " GROUP BY service"

	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statsMap := make(map[string]*ServiceStats)
	var order []string
	for rows.Next() {
		var svc string
		var cnt, errCnt int64
		if err := rows.Scan(&svc, &cnt, &errCnt); err != nil {
			return nil, err
		}
		statsMap[svc] = &ServiceStats{Service: svc, Count: cnt, ErrorCount: errCnt}
		order = append(order, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, svc := range order {
		dq := "SELECT duration_us FROM spans WHERE service = ?"
		dargs := []any{svc}
		if projectID != 0 {
			dq += " AND project_id = ?"
			dargs = append(dargs, projectID)
		}
		if !from.IsZero() {
			dq += " AND ingested_at >= ?"
			dargs = append(dargs, from)
		}
		if !to.IsZero() {
			dq += " AND ingested_at <= ?"
			dargs = append(dargs, to)
		}
		dq += " ORDER BY duration_us"

		drows, err := r.db.Query(dq, dargs...)
		if err != nil {
			return nil, err
		}
		var durations []int64
		for drows.Next() {
			var d int64
			if err := drows.Scan(&d); err != nil {
				drows.Close()
				return nil, err
			}
			durations = append(durations, d)
		}
		drows.Close()
		if err := drows.Err(); err != nil {
			return nil, err
		}
		statsMap[svc].Durations = durations
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
	Durations  []int64
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

	q := `SELECT name, resource, kind, COUNT(*) as cnt,
		SUM(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) as err_cnt
		FROM spans WHERE ` + strings.Join(where, " AND ") + ` GROUP BY name, resource, kind`

	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type opKey struct{ name, resource, kind string }
	statsMap := make(map[opKey]*OperationStats)
	var order []opKey
	for rows.Next() {
		var name, resource, kind string
		var cnt, errCnt int64
		if err := rows.Scan(&name, &resource, &kind, &cnt, &errCnt); err != nil {
			return nil, err
		}
		k := opKey{name, resource, kind}
		statsMap[k] = &OperationStats{Operation: name, Resource: resource, Kind: kind, Count: cnt, ErrorCount: errCnt}
		order = append(order, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, k := range order {
		dq := "SELECT duration_us FROM spans WHERE service = ? AND name = ? AND resource = ? AND kind = ?"
		dargs := []any{service, k.name, k.resource, k.kind}
		if projectID != 0 {
			dq += " AND project_id = ?"
			dargs = append(dargs, projectID)
		}
		if !from.IsZero() {
			dq += " AND ingested_at >= ?"
			dargs = append(dargs, from)
		}
		if !to.IsZero() {
			dq += " AND ingested_at <= ?"
			dargs = append(dargs, to)
		}
		dq += " ORDER BY duration_us"

		drows, err := r.db.Query(dq, dargs...)
		if err != nil {
			return nil, err
		}
		var durations []int64
		for drows.Next() {
			var d int64
			if err := drows.Scan(&d); err != nil {
				drows.Close()
				return nil, err
			}
			durations = append(durations, d)
		}
		drows.Close()
		if err := drows.Err(); err != nil {
			return nil, err
		}
		statsMap[k].Durations = durations
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
	Durations  []int64
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

	q := fmt.Sprintf(`SELECT
		datetime((strftime('%%s', ingested_at) / %d) * %d, 'unixepoch') as bucket,
		duration_us,
		CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END as is_error
		FROM spans WHERE %s
		ORDER BY bucket, duration_us`,
		intervalSec, intervalSec, strings.Join(where, " AND "))

	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bucketMap := make(map[time.Time]*SpanBucket)
	var order []time.Time
	for rows.Next() {
		var bucketStr string
		var dur, isError int64
		if err := rows.Scan(&bucketStr, &dur, &isError); err != nil {
			return nil, err
		}
		t, _ := time.Parse("2006-01-02 15:04:05", bucketStr)
		sb, ok := bucketMap[t]
		if !ok {
			sb = &SpanBucket{Bucket: t}
			bucketMap[t] = sb
			order = append(order, t)
		}
		sb.Count++
		sb.ErrorCount += isError
		sb.Durations = append(sb.Durations, dur)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]SpanBucket, 0, len(order))
	for _, t := range order {
		result = append(result, *bucketMap[t])
	}
	return result, nil
}
