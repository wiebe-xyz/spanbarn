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
	return r.execLow(func() error {
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
	})
}

// appendCommonWhere appends the optional span filters shared by the span/trace
// query methods — project, service, operation, status, minimum duration and the
// ingested_at time range — as parameterised predicates. Method-specific filters
// (trace ID, operation exclusions) are added separately by the caller. All
// predicates are ANDed, so the append order does not affect results.
func (f SpanFilter) appendCommonWhere(where []string, args []any) ([]string, []any) {
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
	return where, args
}

func (r *Repository) QuerySpans(f SpanFilter) ([]Span, error) {
	var where []string
	var args []any

	where, args = f.appendCommonWhere(where, args)
	if f.TraceID != "" {
		where = append(where, "trace_id = ?")
		args = append(args, f.TraceID)
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
	var n int64
	err := r.execLow(func() error {
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		q := "DELETE FROM spans WHERE id IN (" + strings.Join(placeholders, ",") + ")"
		res, e := r.db.Exec(q, args...)
		if e != nil {
			return e
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

func (r *Repository) DeleteSpansByMaxID(maxID int64) (int64, error) {
	return r.execLowAffecting("DELETE FROM spans WHERE id <= ?", maxID)
}

func (r *Repository) DeleteBoringTraces(olderThan, newerThan time.Time, slowThresholdUS int64) (int64, error) {
	var total int64
	for {
		var n int64
		err := r.execLow(func() error {
			res, e := r.db.Exec(`DELETE FROM spans WHERE trace_id IN (
				SELECT trace_id FROM spans
				WHERE ingested_at < ? AND ingested_at >= ?
				GROUP BY trace_id
				HAVING MAX(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) = 0
				AND MAX(duration_us) <= ?
				LIMIT 1000)`,
				olderThan, newerThan, slowThresholdUS)
			if e != nil {
				return e
			}
			n, _ = res.RowsAffected()
			return nil
		})
		if err != nil {
			return total, err
		}
		total += n
		if n == 0 {
			break
		}
	}
	return total, nil
}

func (r *Repository) DeleteSpansOlderThan(cutoff time.Time) (int64, error) {
	return r.execLowAffecting("DELETE FROM spans WHERE ingested_at < ?", cutoff)
}

// DeleteBoringSpansOlderThan removes non-error, fast spans ingested before cutoff.
// Uses idx_spans_ingested (ingested_at) for the range scan. The delete is batched
// (retentionDeleteBatch rows per statement) so a large purge never holds the
// write lock long enough to block the WAL checkpoint or read-only queries.
func (r *Repository) DeleteBoringSpansOlderThan(ctx context.Context, cutoff time.Time, slowThresholdUs int64) (int64, error) {
	return r.batchedDelete(ctx, func() (int64, error) {
		res, e := r.db.ExecContext(ctx,
			`DELETE FROM spans WHERE rowid IN (
				SELECT rowid FROM spans
				WHERE ingested_at < ? AND status NOT IN ('error','ERROR','Error') AND duration_us < ?
				LIMIT ?)`,
			cutoff, slowThresholdUs, retentionDeleteBatch,
		)
		if e != nil {
			return 0, e
		}
		n, _ := res.RowsAffected()
		return n, nil
	})
}

func (r *Repository) CountSpansOlderThan(cutoff time.Time) (int64, error) {
	ctx, cancel := r.queryContext()
	defer cancel()
	var n int64
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM spans WHERE ingested_at <= ?", cutoff).Scan(&n)
	return n, err
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

// TraceSummaryRow is the per-trace summary produced by SearchTraceSummaries.
// Keeping this in the repo layer lets the SQL aggregate everything instead of
// shipping raw spans to Go for a group-by.
type TraceSummaryRow struct {
	TraceID      string
	StartTimeUs  int64
	SpanCount    int
	HasError     bool
	RootName     string
	RootService  string
	RootDuration int64
	RootModel    string
	PromptCount  int
}

// SearchTraceSummaries returns at most filter.Limit trace summaries matching
// the filter, ordered by trace start time descending. The grouping happens
// in SQL — old code fetched filter.Limit*5 raw spans and grouped in Go,
// which was the dominant cost of the /traces page.
//
// minSpans (>0) filters out traces whose total span count falls below the
// threshold; applied via HAVING so pagination respects it.
//
// Considers both `spans` and `error_samples` because old traces past span
// retention may only have error_samples rows; without the UNION those
// traces would disappear from search even though detail still works.
func (r *Repository) SearchTraceSummaries(f SpanFilter, minSpans int) ([]TraceSummaryRow, error) {
	var where []string
	var args []any
	where, args = f.appendCommonWhere(where, args)
	if len(f.ExcludeOperations) > 0 {
		placeholders := strings.Repeat("?,", len(f.ExcludeOperations))
		placeholders = placeholders[:len(placeholders)-1]
		where = append(where, "name NOT IN ("+placeholders+")")
		for _, op := range f.ExcludeOperations {
			args = append(args, op)
		}
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	having := ""
	if minSpans > 0 {
		having = fmt.Sprintf(" HAVING COUNT(DISTINCT span_id) >= %d", minSpans)
	}
	if f.RootOnly {
		rootCheck := "MAX(CASE WHEN COALESCE(parent_span_id,'') = '' THEN 1 ELSE 0 END) = 1"
		if having == "" {
			having = " HAVING " + rootCheck
		} else {
			having += " AND " + rootCheck
		}
	}

	// Args are reused for the two UNIONed selects (spans + error_samples).
	doubled := make([]any, 0, len(args)*2)
	doubled = append(doubled, args...)
	doubled = append(doubled, args...)

	orderBy := "start_us DESC"
	if f.SortErrorsFirst {
		orderBy = "has_error DESC, start_us DESC"
	}

	q := fmt.Sprintf(`
		SELECT trace_id,
		       MIN(start_time_us)                                                  AS start_us,
		       COUNT(DISTINCT span_id)                                             AS span_count,
		       MAX(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) AS has_error
		FROM (
			SELECT trace_id, span_id, status, start_time_us, COALESCE(parent_span_id,'') AS parent_span_id FROM spans%s
			UNION ALL
			SELECT trace_id, span_id, status, start_time_us, COALESCE(parent_span_id,'') AS parent_span_id FROM error_samples%s
		)
		GROUP BY trace_id%s
		ORDER BY `+orderBy+`
		LIMIT %d OFFSET %d`,
		whereSQL, whereSQL, having, limit, f.Offset)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, doubled...)
	if err != nil {
		return nil, err
	}
	type acc struct {
		startUs   int64
		spanCount int
		hasError  bool
	}
	order := make([]string, 0, limit)
	accs := make(map[string]*acc, limit)
	for rows.Next() {
		var traceID string
		var startUs int64
		var spanCount int
		var hasErrorInt int
		if err := rows.Scan(&traceID, &startUs, &spanCount, &hasErrorInt); err != nil {
			rows.Close()
			return nil, err
		}
		order = append(order, traceID)
		accs[traceID] = &acc{startUs: startUs, spanCount: spanCount, hasError: hasErrorInt == 1}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, nil
	}

	// Step 2: look up the root span (parent_span_id NULL/'') per trace.
	// Limited to the page we just fetched, so this is cheap.
	placeholders := strings.Repeat("?,", len(order))
	placeholders = placeholders[:len(placeholders)-1]
	rootArgs := make([]any, 0, len(order)*2)
	for _, t := range order {
		rootArgs = append(rootArgs, t)
	}
	for _, t := range order {
		rootArgs = append(rootArgs, t)
	}
	rootQ := fmt.Sprintf(`
		SELECT trace_id, name, service, duration_us, start_time_us, COALESCE(parent_span_id,'') AS parent
		FROM (
			SELECT trace_id, name, service, duration_us, start_time_us, parent_span_id
			FROM spans WHERE trace_id IN (%s)
			UNION ALL
			SELECT trace_id, name, service, duration_us, start_time_us, parent_span_id
			FROM error_samples WHERE trace_id IN (%s)
		)
		ORDER BY (CASE WHEN COALESCE(parent_span_id,'') = '' THEN 0 ELSE 1 END), start_time_us`,
		placeholders, placeholders)

	rootCtx, rootCancel := r.queryContext()
	defer rootCancel()
	rrows, err := r.db.QueryContext(rootCtx, rootQ, rootArgs...)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()

	type rootInfo struct {
		name     string
		service  string
		duration int64
		isRoot   bool
	}
	roots := make(map[string]rootInfo, len(order))
	for rrows.Next() {
		var traceID, name, service, parent string
		var dur, startUs int64
		if err := rrows.Scan(&traceID, &name, &service, &dur, &startUs, &parent); err != nil {
			return nil, err
		}
		existing, ok := roots[traceID]
		// Prefer a real root (parent==""), else first-seen.
		isRoot := parent == ""
		if !ok || (isRoot && !existing.isRoot) {
			roots[traceID] = rootInfo{name: name, service: service, duration: dur, isRoot: isRoot}
		}
	}
	if err := rrows.Err(); err != nil {
		return nil, err
	}

	// Step 3: fetch model and prompt count from prompt_records for each trace.
	type promptInfo struct {
		model string
		count int
	}
	promptsByTrace := make(map[string]promptInfo, len(order))
	if len(order) > 0 {
		prPlaceholders := strings.Repeat("?,", len(order))
		prPlaceholders = prPlaceholders[:len(prPlaceholders)-1]
		prArgs := make([]any, len(order))
		for i, t := range order {
			prArgs[i] = t
		}
		prQ := fmt.Sprintf(`SELECT trace_id, MIN(model), COUNT(*) FROM prompt_records WHERE trace_id IN (%s) GROUP BY trace_id`, prPlaceholders)
		prCtx, prCancel := r.queryContext()
		defer prCancel()
		prRows, err := r.db.QueryContext(prCtx, prQ, prArgs...)
		if err == nil {
			for prRows.Next() {
				var tid, model string
				var cnt int
				if err := prRows.Scan(&tid, &model, &cnt); err == nil {
					promptsByTrace[tid] = promptInfo{model: model, count: cnt}
				}
			}
			prRows.Close()
		}
	}

	out := make([]TraceSummaryRow, 0, len(order))
	for _, traceID := range order {
		a := accs[traceID]
		ri := roots[traceID]
		pi := promptsByTrace[traceID]
		out = append(out, TraceSummaryRow{
			TraceID:      traceID,
			StartTimeUs:  a.startUs,
			SpanCount:    a.spanCount,
			HasError:     a.hasError,
			RootName:     ri.name,
			RootService:  ri.service,
			RootDuration: ri.duration,
			RootModel:    pi.model,
			PromptCount:  pi.count,
		})
	}
	return out, nil
}

func (r *Repository) StreamSpans(f SpanFilter, fn func(Span) error) error {
	var where []string
	var args []any

	where, args = f.appendCommonWhere(where, args)

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
	var where []string
	var args []any

	where = append(where, "COALESCE(parent_span_id,'') = ''")

	if f.ProjectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Service != "" {
		where = append(where, "service = ?")
		args = append(args, f.Service)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.MinDuration > 0 {
		where = append(where, "duration_us >= ?")
		args = append(args, f.MinDuration)
	}
	if len(f.ExcludeOperations) > 0 {
		placeholders := strings.Repeat("?,", len(f.ExcludeOperations))
		placeholders = placeholders[:len(placeholders)-1]
		where = append(where, "name NOT IN ("+placeholders+")")
		for _, op := range f.ExcludeOperations {
			args = append(args, op)
		}
	}
	if !f.From.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, f.To)
	}

	whereClause := "WHERE " + strings.Join(where, " AND ")

	q := fmt.Sprintf(`
		SELECT name, service, duration_us,
		       CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END AS is_error
		FROM spans
		%s
		ORDER BY name, service, duration_us`, whereClause)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type groupKey struct{ operation, service string }
	type groupVal struct {
		count      int64
		errorCount int64
		durations  []int64
	}
	byOp := make(map[groupKey]*groupVal)
	var order []groupKey
	for rows.Next() {
		var name, service string
		var dur int64
		var isError int
		if err := rows.Scan(&name, &service, &dur, &isError); err != nil {
			return nil, err
		}
		k := groupKey{name, service}
		v, ok := byOp[k]
		if !ok {
			v = &groupVal{}
			byOp[k] = v
			order = append(order, k)
		}
		v.count++
		v.durations = append(v.durations, dur)
		if isError == 1 {
			v.errorCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]RootSpanGroup, 0, len(order))
	for _, k := range order {
		v := byOp[k]
		out = append(out, RootSpanGroup{
			Operation:  k.operation,
			Service:    k.service,
			Count:      v.count,
			ErrorCount: v.errorCount,
			Durations:  v.durations,
		})
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
		values         []int64
		good, ni, poor int64
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
