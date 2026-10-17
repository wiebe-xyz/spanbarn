package repository

import "strings"

// SpanCount is what CountSpans returns: the matching spans, the error spans
// among them, and the sample weights they carry.
type SpanCount struct {
	Total   int64
	Errors  int64
	Weights SpanWeights
}

// CountSpans counts the spans matching f and, of those, the ones with an error
// status, and sums their sample weights. It applies the same predicates as
// QuerySpans (project, time range, attribute expression), so an SLO filter
// built for the span list counts the same rows. An expression without a From
// bound is refused.
func (r *Repository) CountSpans(f SpanFilter) (SpanCount, error) {
	var where []string
	var args []any
	where, args = f.appendCommonWhere(where, args)
	where, args, err := f.appendExprWhere(where, args)
	if err != nil {
		return SpanCount{}, err
	}
	q := `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN LOWER(status) = 'error' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN sample_weight > 0 THEN sample_weight ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN sample_weight > 0 THEN 1 ELSE 0 END), 0)
		FROM spans`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	ctx, cancel := r.queryContext()
	defer cancel()
	var c SpanCount
	err = r.db.QueryRowContext(ctx, q, args...).Scan(&c.Total, &c.Errors, &c.Weights.Count, &c.Weights.Spans)
	return c, err
}
