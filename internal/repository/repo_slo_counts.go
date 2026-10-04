package repository

import "strings"

// CountSpans counts the spans matching f and, of those, the ones with an error
// status. It applies the same predicates as QuerySpans (project, time range,
// attribute expression), so an SLO filter built for the span list counts the
// same rows. An expression without a From bound is refused.
func (r *Repository) CountSpans(f SpanFilter) (total, errors int64, err error) {
	var where []string
	var args []any
	where, args = f.appendCommonWhere(where, args)
	where, args, err = f.appendExprWhere(where, args)
	if err != nil {
		return 0, 0, err
	}
	q := `SELECT COUNT(*), COALESCE(SUM(CASE WHEN LOWER(status) = 'error' THEN 1 ELSE 0 END), 0) FROM spans`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	ctx, cancel := r.queryContext()
	defer cancel()
	err = r.db.QueryRowContext(ctx, q, args...).Scan(&total, &errors)
	return total, errors, err
}
