package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

// Calculation names for AnalyzeCalc.Fn.
const (
	CalcCount         = "count"
	CalcErrorRate     = "error_rate"
	CalcSumDuration   = "sum_duration"
	CalcAvgDuration   = "avg_duration"
	CalcMaxDuration   = "max_duration"
	CalcP50           = "p50"
	CalcP95           = "p95"
	CalcP99           = "p99"
	CalcCountDistinct = "count_distinct"
)

// AnalyzeCalc is one column of a group-by query. Key names the attribute for
// count_distinct and is empty for every other calculation.
type AnalyzeCalc struct {
	Fn  string
	Key string
}

// AnalyzeQuery is a group-by query over the spans of one project. ProjectID,
// From, To, Calcs and MaxSpans are required. Durations are microseconds.
type AnalyzeQuery struct {
	ProjectID int64
	From, To  time.Time
	Expr      *filter.Expr
	GroupBy   []string
	Calcs     []AnalyzeCalc
	// OrderBy indexes Calcs. Desc sorts high to low. Limit caps the groups
	// returned by a table query; the rest fold into Other.
	OrderBy int
	Desc    bool
	Limit   int
	// BucketSeconds above zero makes a time series: one row per bucket and
	// group. Only then lists the groups to keep, every other group folds into
	// one Other row per bucket.
	BucketSeconds int64
	Only          [][]string
	// Sample keeps spans with id % Sample = 0. 1 reads every span and 0 picks
	// the smallest ratio that keeps the scan under MaxSpans.
	Sample int
	// MaxSpans caps how many spans the scan reads, newest first.
	MaxSpans int

	// calc resolves the project's calculated fields in Expr, GroupBy and the
	// count_distinct keys. Analyze sets it.
	calc filter.Resolver
}

// AnalyzeRow is one group, or one group in one bucket. Values follow
// AnalyzeQuery.Calcs. Counts and sums are over the scanned spans, not scaled
// for Sample.
type AnalyzeRow struct {
	// Bucket is the bucket start in unix seconds, series only.
	Bucket int64
	Group  []string
	Other  bool
	Count  int64
	Values []float64
}

// AnalyzeResult holds the rows of a group-by query. Other is the table query's
// row for the groups beyond Limit and is nil when none were cut.
type AnalyzeResult struct {
	Rows  []AnalyzeRow
	Other *AnalyzeRow
	// Scanned is the number of spans read after sampling and SampleEvery the
	// ratio used. Truncated is true when the scan stopped at MaxSpans.
	Scanned     int64
	SampleEvery int
	Truncated   bool
}

const maxAnalyzeSample = 1000

// scanWhere is the predicate and arguments that select the spans a query reads.
func (q AnalyzeQuery) scanWhere(sample int) (string, []any, error) {
	where := []string{"project_id = ?", "ingested_at >= ?", "ingested_at <= ?"}
	args := []any{q.ProjectID, q.From, q.To}
	if q.Expr != nil && len(q.Expr.Filters) > 0 {
		pred, a, err := filter.CompileWith(q.Expr, q.calc)
		if err != nil {
			return "", nil, err
		}
		where = append(where, pred)
		args = append(args, a...)
	}
	if sample > 1 {
		where = append(where, "id % ? = 0")
		args = append(args, sample)
	}
	return strings.Join(where, " AND "), args, nil
}

// baseSelect reads the scanned spans as the columns the aggregation needs: a
// text key per group-by entry, duration d, error flag e, one text column per
// distinct-counted attribute and the bucket b of a series. The LIMIT keeps
// SQLite from flattening the CTE and enforces the row cap newest first.
func (q AnalyzeQuery) baseSelect(sample int) (string, []any, error) {
	var cols []string
	var args []any
	for i, k := range q.GroupBy {
		s, a, err := filter.TextSQLWith(k, q.calc)
		if err != nil {
			return "", nil, err
		}
		cols = append(cols, fmt.Sprintf("COALESCE(%s, '') AS g%d", s, i))
		args = append(args, a...)
	}
	cols = append(cols, "duration_us AS d", "CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END AS e")
	for i, k := range q.distinctKeys() {
		s, a, err := filter.TextSQLWith(k, q.calc)
		if err != nil {
			return "", nil, err
		}
		cols = append(cols, fmt.Sprintf("%s AS c%d", s, i))
		args = append(args, a...)
	}
	if q.BucketSeconds > 0 {
		cols = append(cols, "(CAST(strftime('%s', ingested_at) AS INTEGER) / ?) * ? AS b")
		args = append(args, q.BucketSeconds, q.BucketSeconds)
	}
	where, wargs, err := q.scanWhere(sample)
	if err != nil {
		return "", nil, err
	}
	args = append(args, wargs...)
	args = append(args, q.MaxSpans)
	return "SELECT " + strings.Join(cols, ", ") + " FROM spans WHERE " + where +
		" ORDER BY ingested_at DESC LIMIT ?", args, nil
}

// distinctKeys lists the attributes of the count_distinct calculations once each.
func (q AnalyzeQuery) distinctKeys() []string {
	var keys []string
	seen := map[string]bool{}
	for _, c := range q.Calcs {
		if c.Fn == CalcCountDistinct && !seen[c.Key] {
			seen[c.Key] = true
			keys = append(keys, c.Key)
		}
	}
	return keys
}

func (q AnalyzeQuery) distinctIndex(key string) int {
	for i, k := range q.distinctKeys() {
		if k == key {
			return i
		}
	}
	return -1
}

// percentiles lists the percentile values the calculations ask for.
func (q AnalyzeQuery) percentiles() []int {
	var out []int
	for _, c := range q.Calcs {
		if p, ok := percentileOf(c.Fn); ok {
			out = append(out, p)
		}
	}
	return out
}

func percentileOf(fn string) (int, bool) {
	switch fn {
	case CalcP50:
		return 50, true
	case CalcP95:
		return 95, true
	case CalcP99:
		return 99, true
	}
	return 0, false
}

// countScanned counts the spans a scan would read, stopping at limit.
func (r *Repository) countScanned(ctx context.Context, q AnalyzeQuery, sample, limit int) (int64, error) {
	where, args, err := q.scanWhere(sample)
	if err != nil {
		return 0, err
	}
	var n int64
	err = r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM (SELECT 1 FROM spans WHERE "+where+" LIMIT ?)", append(args, limit)...).Scan(&n)
	return n, err
}

// chooseSample returns the 1-in-N ratio for a query. An automatic query reads
// every span when the match fits under MaxSpans. Otherwise it estimates the
// match from a sampled count so a selective filter loses little precision.
func (r *Repository) chooseSample(ctx context.Context, q AnalyzeQuery) (int, error) {
	if q.Sample > 0 {
		return min(q.Sample, maxAnalyzeSample), nil
	}
	n, err := r.countScanned(ctx, q, 1, q.MaxSpans+1)
	if err != nil || n <= int64(q.MaxSpans) {
		return 1, err
	}
	var window int64
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM spans WHERE project_id = ? AND ingested_at >= ? AND ingested_at <= ?",
		q.ProjectID, q.From, q.To).Scan(&window); err != nil {
		return 0, err
	}
	probe := clampInt64(ceilDiv(window, int64(q.MaxSpans)), 2, maxAnalyzeSample)
	hits, err := r.countScanned(ctx, q, int(probe), q.MaxSpans+1)
	if err != nil {
		return 0, err
	}
	return int(clampInt64(ceilDiv(hits*probe, int64(q.MaxSpans)), 2, maxAnalyzeSample)), nil
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

func clampInt64(v, lo, hi int64) int64 { return max(lo, min(v, hi)) }
