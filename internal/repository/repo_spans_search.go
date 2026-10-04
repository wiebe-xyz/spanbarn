package repository

import (
	"database/sql"
	"fmt"
	"strings"
)

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
	// HasRoot is nil while the summary's structure is not computed yet.
	HasRoot *bool
	// OrphanCount is the number of spans whose parent is absent from the trace.
	OrphanCount int
}

// SearchTraceSummaries returns at most filter.Limit trace summaries matching the
// filter, ordered by ingested_at descending (or errors-first). It reads the
// pre-rolled trace_summaries table — one indexed row per trace — instead of
// grouping every span in the window, which timed out on busy projects.
//
// Filters map onto the summary's root/rollup columns: Service→root_service,
// Operation/ExcludeOperations→root_name, Status(error/ok)→has_error,
// MinDuration→root_duration_us (whole-trace duration), From/To→ingested_at,
// minSpans→span_count. Error traces stay listed until the error cutoff because
// their summaries are retained that long (see repo_trace_summaries.go), so
// dropping the old spans∪error_samples UNION does not lose them.
func (r *Repository) SearchTraceSummaries(f SpanFilter, minSpans int) ([]TraceSummaryRow, error) {
	f, err := r.withCalc(f)
	if err != nil {
		return nil, err
	}
	where, args, err := f.traceSummaryWhere(minSpans)
	if err != nil {
		return nil, err
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	orderBy := "ingested_at DESC"
	if f.SortErrorsFirst {
		orderBy = "has_error DESC, ingested_at DESC"
	}

	q := fmt.Sprintf(`SELECT trace_id, start_time_us, span_count, has_error, root_name, root_service, root_duration_us, has_root, orphan_count
		FROM trace_summaries%s
		ORDER BY %s
		LIMIT %d OFFSET %d`, whereSQL, orderBy, limit, f.Offset)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	order, byTrace, err := scanTraceSummaryRows(rows, limit)
	if err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, nil
	}

	r.attachPromptStats(order, byTrace)

	out := make([]TraceSummaryRow, 0, len(order))
	for _, t := range order {
		out = append(out, *byTrace[t])
	}
	return out, nil
}

// traceSummaryWhere builds the trace_summaries WHERE conditions for a filter.
func (f SpanFilter) traceSummaryWhere(minSpans int) ([]string, []any, error) {
	b := &whereBuilder{}
	if f.ProjectID != 0 {
		b.add("project_id = ?", f.ProjectID)
	}
	if f.Service != "" {
		b.add("root_service = ?", f.Service)
	}
	if f.Operation != "" {
		b.add("root_name = ?", f.Operation)
	}
	if f.Status != "" {
		if strings.EqualFold(f.Status, "error") {
			b.add("has_error = 1")
		} else {
			b.add("has_error = 0")
		}
	}
	if f.MinDuration > 0 {
		b.add("root_duration_us >= ?", f.MinDuration)
	}
	b.addTimeRange("ingested_at", f.From, f.To)
	b.addNotIn("root_name", f.ExcludeOperations)
	if minSpans > 0 {
		b.add("span_count >= ?", minSpans)
	}
	where := f.appendStructureWhere(b.where)
	return f.appendTraceExprWhere(where, b.args)
}

// scanTraceSummaryRows reads the summary page and closes rows. It returns the
// trace ids in result order and the rows keyed by trace id.
func scanTraceSummaryRows(rows *sql.Rows, limit int) ([]string, map[string]*TraceSummaryRow, error) {
	order := make([]string, 0, limit)
	byTrace := make(map[string]*TraceSummaryRow, limit)
	for rows.Next() {
		var tr TraceSummaryRow
		var hasErrorInt int
		var hasRoot, orphans sql.NullInt64
		if err := rows.Scan(&tr.TraceID, &tr.StartTimeUs, &tr.SpanCount, &hasErrorInt,
			&tr.RootName, &tr.RootService, &tr.RootDuration, &hasRoot, &orphans); err != nil {
			rows.Close()
			return nil, nil, err
		}
		tr.HasError = hasErrorInt == 1
		tr.HasRoot, tr.OrphanCount = structureFromColumns(hasRoot, orphans)
		row := tr
		order = append(order, tr.TraceID)
		byTrace[tr.TraceID] = &row
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return order, byTrace, nil
}

// attachPromptStats enriches the page with model and prompt count from
// prompt_records. A failing enrichment query leaves the rows without them.
func (r *Repository) attachPromptStats(order []string, byTrace map[string]*TraceSummaryRow) {
	placeholders := strings.Repeat("?,", len(order))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(order))
	for i, t := range order {
		args[i] = t
	}
	q := fmt.Sprintf(`SELECT trace_id, MIN(model), COUNT(*) FROM prompt_records WHERE trace_id IN (%s) GROUP BY trace_id`, placeholders)
	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.reader(FamilyPrompts).QueryContext(ctx, q, args...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var tid, model string
		var cnt int
		if err := rows.Scan(&tid, &model, &cnt); err != nil {
			continue
		}
		if row := byTrace[tid]; row != nil {
			row.RootModel = model
			row.PromptCount = cnt
		}
	}
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
