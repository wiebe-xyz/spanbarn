package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ServiceStats holds per-service metrics computed from raw spans.
type ServiceStats struct {
	Service    string
	Count      int64
	ErrorCount int64
	P50Us      int64
	P95Us      int64
	P99Us      int64
}

func (r *Repository) QueryServiceStatsFromSpans(projectID int64, from, to time.Time, kind string) ([]ServiceStats, error) {
	var where []string
	var args []any

	if projectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
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

func (r *Repository) QueryOperationStatsFromSpans(projectID int64, service string, from, to time.Time, kind string) ([]OperationStats, error) {
	var where []string
	var args []any

	where = append(where, "service = ?")
	args = append(args, service)

	if projectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
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

// QueryRootSpanGroups aggregates root spans (parent_span_id = ”) by operation name,
// returning count, error count, and raw durations for percentile computation.
// Uses the idx_spans_root_ingested partial index for efficient scanning.
func (r *Repository) QueryRootSpanGroups(ctx context.Context, f SpanFilter) ([]RootSpanGroup, error) {
	where, args := f.rootSpanGroupWhere()

	q := fmt.Sprintf(`
		SELECT name, service, duration_us,
		       CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END AS is_error
		FROM spans
		WHERE %s
		ORDER BY name, service, duration_us`, strings.Join(where, " AND "))

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return groupRootSpanRows(rows)
}

// rootSpanGroupWhere builds the WHERE conditions for QueryRootSpanGroups.
func (f SpanFilter) rootSpanGroupWhere() ([]string, []any) {
	b := &whereBuilder{}
	b.add("COALESCE(parent_span_id,'') = ''")
	if f.ProjectID != 0 {
		b.add("project_id = ?", f.ProjectID)
	}
	if f.Service != "" {
		b.add("service = ?", f.Service)
	}
	if f.Status != "" {
		b.add("status = ?", f.Status)
	}
	if f.MinDuration > 0 {
		b.add("duration_us >= ?", f.MinDuration)
	}
	b.addNotIn("name", f.ExcludeOperations)
	b.addTimeRange("ingested_at", f.From, f.To)
	return b.where, b.args
}

// groupRootSpanRows folds the (name, service, duration, is_error) rows into one
// group per operation and service, keeping first-seen order.
func groupRootSpanRows(rows *sql.Rows) ([]RootSpanGroup, error) {
	type groupKey struct{ operation, service string }
	byOp := make(map[groupKey]*RootSpanGroup)
	var order []groupKey
	for rows.Next() {
		var name, service string
		var dur int64
		var isError int
		if err := rows.Scan(&name, &service, &dur, &isError); err != nil {
			return nil, err
		}
		k := groupKey{name, service}
		g, ok := byOp[k]
		if !ok {
			g = &RootSpanGroup{Operation: name, Service: service}
			byOp[k] = g
			order = append(order, k)
		}
		g.Count++
		g.Durations = append(g.Durations, dur)
		if isError == 1 {
			g.ErrorCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]RootSpanGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *byOp[k])
	}
	return out, nil
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
