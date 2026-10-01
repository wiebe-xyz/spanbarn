package repository

import (
	"fmt"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

// exprPredicate compiles f.Expr. It refuses an expression without a From bound
// because attribute predicates read the JSON of every span in the window.
func (f SpanFilter) exprPredicate() (string, []any, error) {
	if f.Expr == nil || len(f.Expr.Filters) == 0 {
		return "", nil, nil
	}
	if f.From.IsZero() {
		return "", nil, fmt.Errorf("%w: a time range (from) is required", filter.ErrInvalid)
	}
	return filter.Compile(f.Expr)
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
