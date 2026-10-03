package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// aggPlan describes one aggregation over the base CTE.
type aggPlan struct {
	// keyed folds groups outside Only into NULL keys (series).
	keyed bool
	// notIn drops these group tuples first (table "other" row).
	notIn [][]string
	// keys are the columns to group by, in output order.
	keys  []string
	order string
	limit int
}

// tupleIn renders "(a, b) IN (VALUES ...)" over cols. An empty tuple list is
// never true for IN and always true for NOT IN.
func tupleIn(cols []string, tuples [][]string, negate bool) (string, []any) {
	if len(tuples) == 0 {
		if negate {
			return "1", nil
		}
		return "0", nil
	}
	op := " IN "
	if negate {
		op = " NOT IN "
	}
	var args []any
	if len(cols) == 1 {
		marks := make([]string, len(tuples))
		for i, t := range tuples {
			marks[i] = "?"
			args = append(args, t[0])
		}
		return cols[0] + op + "(" + strings.Join(marks, ",") + ")", args
	}
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
	rows := make([]string, len(tuples))
	for i, t := range tuples {
		rows[i] = row
		for _, v := range t {
			args = append(args, v)
		}
	}
	return "(" + strings.Join(cols, ",") + ")" + op + "(VALUES " + strings.Join(rows, ",") + ")", args
}

func (q AnalyzeQuery) groupCols(prefix string) []string {
	cols := make([]string, len(q.GroupBy))
	for i := range q.GroupBy {
		cols[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return cols
}

// passthrough lists the base columns the later stages keep besides the keys.
func (q AnalyzeQuery) passthrough() string {
	cols := []string{"d", "e"}
	for i := range q.distinctKeys() {
		cols = append(cols, fmt.Sprintf("c%d", i))
	}
	if q.BucketSeconds > 0 {
		cols = append(cols, "b")
	}
	return strings.Join(cols, ", ")
}

// pctColumns lists the percentiles to compute, in column order.
func (q AnalyzeQuery) pctColumns() []int {
	var out []int
	for _, p := range []int{50, 95, 99} {
		for _, got := range q.percentiles() {
			if got == p {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// build renders the aggregation as a WITH chain over the base select.
func (q AnalyzeQuery) build(base string, baseArgs []any, p aggPlan) (string, []any) {
	stages := []string{"base AS (" + base + ")"}
	args := append([]any{}, baseArgs...)
	cur := "base"
	if p.keyed {
		member, a := tupleIn(q.groupCols("g"), q.Only, false)
		cols := make([]string, len(q.GroupBy))
		for i := range q.GroupBy {
			cols[i] = fmt.Sprintf("CASE WHEN %s THEN g%d END AS k%d", member, i, i)
			args = append(args, a...)
		}
		stages = append(stages, "keyed AS (SELECT "+strings.Join(cols, ", ")+", "+q.passthrough()+" FROM base)")
		cur = "keyed"
	}
	if len(p.notIn) > 0 {
		cond, a := tupleIn(q.groupCols("g"), p.notIn, true)
		stages = append(stages, "filtered AS (SELECT * FROM "+cur+" WHERE "+cond+")")
		args = append(args, a...)
		cur = "filtered"
	}
	pcts := q.pctColumns()
	if len(pcts) > 0 {
		part := "NULL"
		if len(p.keys) > 0 {
			part = strings.Join(p.keys, ", ")
		}
		stages = append(stages, "ranked AS (SELECT *, ROW_NUMBER() OVER (PARTITION BY "+part+
			" ORDER BY d) AS rn, COUNT(*) OVER (PARTITION BY "+part+") AS n FROM "+cur+")")
		cur = "ranked"
	}
	sel := append([]string{}, p.keys...)
	sel = append(sel, "COUNT(*) AS cnt", "SUM(e) AS errs", "SUM(d) AS sumd", "MAX(d) AS maxd")
	for i := range q.distinctKeys() {
		sel = append(sel, fmt.Sprintf("COUNT(DISTINCT c%d) AS cd%d", i, i))
	}
	for _, pc := range pcts {
		sel = append(sel, fmt.Sprintf("MAX(CASE WHEN rn = (n * %d + 99) / 100 THEN d END) AS p%d", pc, pc))
	}
	query := "WITH " + strings.Join(stages, ", ") + " SELECT " + strings.Join(sel, ", ") + " FROM " + cur
	if len(p.keys) > 0 {
		query += " GROUP BY " + strings.Join(p.keys, ", ")
	}
	if p.order != "" {
		query += " ORDER BY " + p.order
	}
	if p.limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", p.limit)
	}
	return query, args
}

// orderBySQL sorts a table query by its OrderBy calculation, then by key.
func (q AnalyzeQuery) orderBySQL(keys []string) string {
	dir := " ASC"
	if q.Desc {
		dir = " DESC"
	}
	var expr string
	c := AnalyzeCalc{Fn: CalcCount}
	if q.OrderBy >= 0 && q.OrderBy < len(q.Calcs) {
		c = q.Calcs[q.OrderBy]
	}
	switch c.Fn {
	case CalcErrorRate:
		expr = "errs * 1.0 / cnt"
	case CalcSumDuration:
		expr = "sumd"
	case CalcAvgDuration:
		expr = "sumd * 1.0 / cnt"
	case CalcMaxDuration:
		expr = "maxd"
	case CalcCountDistinct:
		expr = fmt.Sprintf("cd%d", q.distinctIndex(c.Key))
	default:
		if p, ok := percentileOf(c.Fn); ok {
			expr = fmt.Sprintf("p%d", p)
		} else {
			expr = "cnt"
		}
	}
	return strings.Join(append([]string{expr + dir}, keys...), ", ")
}

// value reads one calculation from an aggregated row of stats.
func (q AnalyzeQuery) value(c AnalyzeCalc, stats []float64) float64 {
	cnt, errs, sum, maxd := stats[0], stats[1], stats[2], stats[3]
	nd := len(q.distinctKeys())
	switch c.Fn {
	case CalcErrorRate:
		return errs / cnt
	case CalcSumDuration:
		return sum
	case CalcAvgDuration:
		return sum / cnt
	case CalcMaxDuration:
		return maxd
	case CalcCountDistinct:
		return stats[4+q.distinctIndex(c.Key)]
	}
	if p, ok := percentileOf(c.Fn); ok {
		for i, got := range q.pctColumns() {
			if got == p {
				return stats[4+nd+i]
			}
		}
	}
	return cnt
}

// run executes an aggregation. series true reads a leading bucket column and
// treats a NULL key as the Other group.
func (r *Repository) runAgg(ctx context.Context, q AnalyzeQuery, query string, args []any, nKeys int, series bool) ([]AnalyzeRow, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nStats := 4 + len(q.distinctKeys()) + len(q.pctColumns())
	var out []AnalyzeRow
	for rows.Next() {
		bucket := sql.NullInt64{}
		keys := make([]sql.NullString, nKeys)
		stats := make([]sql.NullFloat64, nStats)
		dest := make([]any, 0, 1+nKeys+nStats)
		if series {
			dest = append(dest, &bucket)
		}
		for i := range keys {
			dest = append(dest, &keys[i])
		}
		for i := range stats {
			dest = append(dest, &stats[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		vals := make([]float64, nStats)
		for i, s := range stats {
			vals[i] = s.Float64
		}
		if vals[0] == 0 {
			continue
		}
		row := AnalyzeRow{Bucket: bucket.Int64, Count: int64(vals[0]), Group: make([]string, nKeys)}
		for i, k := range keys {
			row.Group[i] = k.String
			if series && !k.Valid {
				row.Other = true
			}
		}
		for _, c := range q.Calcs {
			row.Values = append(row.Values, q.value(c, vals))
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Analyze runs a group-by query. A table query returns the top Limit groups by
// the OrderBy calculation and one Other row for the rest. A series query
// returns one row per bucket and group, with groups outside Only folded into
// Other.
func (r *Repository) Analyze(ctx context.Context, q AnalyzeQuery) (*AnalyzeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	var err error
	if q.calc, err = r.calcResolver(q.ProjectID); err != nil {
		return nil, err
	}
	sample, err := r.chooseSample(ctx, q)
	if err != nil {
		return nil, err
	}
	base, bargs, err := q.baseSelect(sample)
	if err != nil {
		return nil, err
	}
	res := &AnalyzeResult{SampleEvery: sample}
	if q.BucketSeconds > 0 {
		res.Rows, err = r.analyzeSeries(ctx, q, base, bargs)
	} else {
		res.Rows, res.Other, err = r.analyzeTable(ctx, q, base, bargs)
	}
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		res.Scanned += row.Count
	}
	if res.Other != nil {
		res.Scanned += res.Other.Count
	}
	res.Truncated = res.Scanned >= int64(q.MaxSpans)
	return res, nil
}

func (r *Repository) analyzeSeries(ctx context.Context, q AnalyzeQuery, base string, bargs []any) ([]AnalyzeRow, error) {
	keys := []string{"b"}
	keyed := q.Only != nil && len(q.GroupBy) > 0
	prefix := "g"
	if keyed {
		prefix = "k"
	}
	keys = append(keys, q.groupCols(prefix)...)
	query, args := q.build(base, bargs, aggPlan{keyed: keyed, keys: keys, order: strings.Join(keys, ", ")})
	return r.runAgg(ctx, q, query, args, len(keys)-1, true)
}

func (r *Repository) analyzeTable(ctx context.Context, q AnalyzeQuery, base string, bargs []any) ([]AnalyzeRow, *AnalyzeRow, error) {
	keys := q.groupCols("g")
	plan := aggPlan{keys: keys}
	if len(keys) > 0 {
		plan.order = q.orderBySQL(keys)
		plan.limit = q.Limit
	}
	query, args := q.build(base, bargs, plan)
	top, err := r.runAgg(ctx, q, query, args, len(keys), false)
	if err != nil || len(keys) == 0 || len(top) < q.Limit {
		return top, nil, err
	}
	tuples := make([][]string, len(top))
	for i, row := range top {
		tuples[i] = row.Group
	}
	query, args = q.build(base, bargs, aggPlan{notIn: tuples})
	rest, err := r.runAgg(ctx, q, query, args, 0, false)
	if err != nil || len(rest) == 0 {
		return top, nil, err
	}
	other := rest[0]
	other.Other = true
	return top, &other, nil
}
