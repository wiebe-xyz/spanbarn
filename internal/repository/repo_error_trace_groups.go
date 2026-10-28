package repository

import (
	"context"
	"fmt"
	"strings"
)

// errorTraceGroupsMax caps how many error traces one grouping reads. The
// newest are kept: a window that holds more is summarised by its latest
// traces, which is what a triage view wants.
const errorTraceGroupsMax = 5000

// errorSpanLookupChunk is how many trace ids one error-span lookup binds.
const errorSpanLookupChunk = 500

// ErrorTraceGroup is the error traces of one root operation: every trace in
// the window with at least one error span, whichever span it was.
type ErrorTraceGroup struct {
	Operation       string // root span name; "" for traces whose root was never received
	Service         string
	Durations       []int64 // root durations, for percentiles in the service layer
	ExampleTraceID  string  // the newest trace of the group
	ErrorOperations []ErrorOperation
}

// ErrorOperation is one span operation that errored inside a group's traces.
type ErrorOperation struct {
	Operation      string
	Service        string
	Traces         int64  // traces of the group in which it errored
	ExampleTraceID string // the newest of those traces
}

type errorTraceRow struct {
	traceID, rootName, rootService string
	rootDurUs                      int64
}

// QueryErrorTraceGroups groups the error traces of a window by root operation.
// It reads trace_summaries, so it covers the whole window (spans and
// error_samples alike) and matches a trace by any error span, where
// QueryRootSpanGroups reads root spans in the raw span window only. f.Status is
// ignored: every trace it returns has an error.
func (r *Repository) QueryErrorTraceGroups(ctx context.Context, f SpanFilter) ([]ErrorTraceGroup, error) {
	ctx, cancel := r.readContext(ctx)
	defer cancel()
	f.Status = "error"
	traces, err := r.errorTraces(ctx, f)
	if err != nil || len(traces) == 0 {
		return nil, err
	}
	ids := make([]string, len(traces))
	for i, t := range traces {
		ids[i] = t.traceID
	}
	errOps, err := r.errorSpanOperations(ctx, f.ProjectID, ids)
	if err != nil {
		return nil, err
	}
	return groupErrorTraces(traces, errOps), nil
}

// errorTraces reads the window's error trace summaries, newest first.
func (r *Repository) errorTraces(ctx context.Context, f SpanFilter) ([]errorTraceRow, error) {
	where, args, err := f.traceSummaryWhere(0)
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`SELECT trace_id, root_name, root_service, root_duration_us
		FROM trace_summaries WHERE %s
		ORDER BY ingested_at DESC LIMIT %d`, strings.Join(where, " AND "), errorTraceGroupsMax)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []errorTraceRow
	for rows.Next() {
		var t errorTraceRow
		if err := rows.Scan(&t.traceID, &t.rootName, &t.rootService, &t.rootDurUs); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type spanOp struct{ name, service string }

// errorSpanOperations returns, per trace id, the distinct operations of its
// error spans, from spans and error_samples. Both lookups read only the
// partial error-trace indexes (see spansErrorTraceIndexesUp).
func (r *Repository) errorSpanOperations(ctx context.Context, projectID int64, ids []string) (map[string][]spanOp, error) {
	out := make(map[string][]spanOp, len(ids))
	seen := make(map[string]map[spanOp]bool, len(ids))
	for start := 0; start < len(ids); start += errorSpanLookupChunk {
		chunk := ids[start:min(start+errorSpanLookupChunk, len(ids))]
		if err := r.errorSpanChunk(ctx, projectID, chunk, func(trace string, op spanOp) {
			if seen[trace] == nil {
				seen[trace] = map[spanOp]bool{}
			}
			if !seen[trace][op] {
				seen[trace][op] = true
				out[trace] = append(out[trace], op)
			}
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *Repository) errorSpanChunk(ctx context.Context, projectID int64, ids []string, add func(string, spanOp)) error {
	var args []any
	for _, id := range ids {
		args = append(args, id)
	}
	if projectID != 0 {
		args = append(args, projectID)
	}
	args = append(args, args...)
	rows, err := r.db.QueryContext(ctx, errorSpanQuery(len(ids), projectID != 0, true), args...)
	if err != nil && strings.Contains(err.Error(), "no such index") {
		// A reader can serve before the writer's migration built the indexes.
		rows, err = r.db.QueryContext(ctx, errorSpanQuery(len(ids), projectID != 0, false), args...)
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var trace string
		var op spanOp
		if err := rows.Scan(&trace, &op.name, &op.service); err != nil {
			return err
		}
		add(trace, op)
	}
	return rows.Err()
}

// groupErrorTraces folds traces (newest first) into one group per root
// operation and service, in first-seen order, so each example is the newest.
func groupErrorTraces(traces []errorTraceRow, errOps map[string][]spanOp) []ErrorTraceGroup {
	type groupKey struct{ operation, service string }
	type group struct {
		g     ErrorTraceGroup
		opIdx map[spanOp]int
	}
	byKey := map[groupKey]*group{}
	var order []groupKey
	for _, t := range traces {
		k := groupKey{t.rootName, t.rootService}
		g, ok := byKey[k]
		if !ok {
			g = &group{g: ErrorTraceGroup{Operation: t.rootName, Service: t.rootService, ExampleTraceID: t.traceID}, opIdx: map[spanOp]int{}}
			byKey[k] = g
			order = append(order, k)
		}
		g.g.Durations = append(g.g.Durations, t.rootDurUs)
		for _, op := range errOps[t.traceID] {
			i, ok := g.opIdx[op]
			if !ok {
				i = len(g.g.ErrorOperations)
				g.opIdx[op] = i
				g.g.ErrorOperations = append(g.g.ErrorOperations, ErrorOperation{Operation: op.name, Service: op.service, ExampleTraceID: t.traceID})
			}
			g.g.ErrorOperations[i].Traces++
		}
	}
	out := make([]ErrorTraceGroup, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k].g)
	}
	return out
}

// errorSpanQuery selects the error operations of n traces from spans and
// error_samples, of one project when project is set. pinned names the partial
// error-trace indexes.
func errorSpanQuery(n int, project, pinned bool) string {
	cond := ""
	if project {
		cond = " AND project_id = ?"
	}
	from := func(table, index string) string {
		if pinned {
			return table + " INDEXED BY " + index
		}
		return table
	}
	part := `SELECT trace_id, name, service FROM %s
		WHERE trace_id IN (` + placeholderList(n) + `) AND ` + errorStatusSQL + cond
	return fmt.Sprintf(part, from("spans", "idx_spans_error_trace")) + " UNION " +
		fmt.Sprintf(part, from("error_samples", "idx_error_samples_error_trace"))
}
