package repository

import (
	"fmt"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

// scopedExpr returns f.Expr with the environment condition ANDed on, or f.Expr
// itself when f selects every environment.
func (f SpanFilter) scopedExpr() (*filter.Expr, error) {
	node, ok := filter.EnvironmentNode(f.Environment)
	if !ok {
		return f.Expr, nil
	}
	out, ok := filter.Conjoin(f.Expr, []filter.Node{node})
	if !ok {
		return nil, fmt.Errorf("%w: the environment condition cannot be added to a filter with nested groups under or", filter.ErrInvalid)
	}
	return out, nil
}

// exprPredicate compiles f.Expr with the environment condition. It refuses an
// expression without a From bound because attribute predicates read the JSON of
// every span in the window.
func (f SpanFilter) exprPredicate() (string, []any, error) {
	expr, err := f.scopedExpr()
	if err != nil || expr == nil || len(expr.Filters) == 0 {
		return "", nil, err
	}
	if f.From.IsZero() {
		return "", nil, fmt.Errorf("%w: a time range (from) is required", filter.ErrInvalid)
	}
	return filter.CompileWith(expr, f.calc)
}

// appendEnvironmentWhere ANDs the environment condition alone onto a span
// query that has no expression of its own, such as the dashboard cards and the
// root span groups. The condition compiles from a constant node, so a failure
// is a bug: it selects nothing and a test pins the compiled form.
func (f SpanFilter) appendEnvironmentWhere(where []string, args []any) ([]string, []any) {
	node, ok := filter.EnvironmentNode(f.Environment)
	if !ok {
		return where, args
	}
	pred, a, err := filter.Compile(&filter.Expr{Match: filter.MatchAnd, Filters: []filter.Node{node}})
	if err != nil || pred == "" {
		return append(where, "0 = 1"), args
	}
	return append(where, pred), append(args, a...)
}

// withCalc returns f with the calculated fields of its project, loaded once for
// the query. A filter without an expression needs none.
func (r *Repository) withCalc(f SpanFilter) (SpanFilter, error) {
	if (f.Expr == nil || len(f.Expr.Filters) == 0) && f.Environment == "" {
		return f, nil
	}
	calc, err := r.calcResolver(f.ProjectID)
	f.calc = calc
	return f, err
}

// appendExprWhere ANDs the filter expression onto a span query.
func (f SpanFilter) appendExprWhere(where []string, args []any) ([]string, []any, error) {
	pred, a, err := f.exprPredicate()
	if err != nil || pred == "" {
		return where, args, err
	}
	return append(where, pred), append(args, a...), nil
}

// appendTraceExprWhere keeps the traces that hold a span matching the filter
// expression. The subquery is bounded by project and ingested_at, so it scans
// the same window as the trace list and nothing older.
func (f SpanFilter) appendTraceExprWhere(where []string, args []any) ([]string, []any, error) {
	pred, a, err := f.exprPredicate()
	if err != nil || pred == "" {
		return where, args, err
	}
	sub := []string{"ingested_at >= ?"}
	subArgs := []any{f.From}
	if f.ProjectID != 0 {
		sub = append(sub, "project_id = ?")
		subArgs = append(subArgs, f.ProjectID)
	}
	if !f.To.IsZero() {
		sub = append(sub, "ingested_at <= ?")
		subArgs = append(subArgs, f.To)
	}
	sub = append(sub, pred)
	subArgs = append(subArgs, a...)
	where = append(where, "trace_id IN (SELECT trace_id FROM spans WHERE "+strings.Join(sub, " AND ")+")")
	return where, append(args, subArgs...), nil
}
