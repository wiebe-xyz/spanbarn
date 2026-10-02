package repository

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Dashboard queries read raw spans over a short window (retention keeps ~48h)
// and bucket on ingested_at, the same server-authoritative key the span indexes
// and retention use.

// dashboardRowCap bounds how many span rows a single percentile query pulls
// into memory. It is far above a 48h window of normal traffic and exists so a
// runaway project cannot exhaust the reader.
const dashboardRowCap = 5_000_000

// dashboardSpans is the spans table pinned to the covering index of migration
// 038. Without ANALYZE stats the planner prefers indexes with an equality prefix
// (project_id, status) that are not covering, and each row then costs a random
// page read in the wide spans table. The index holds every column these queries
// read, so the table is never touched.
const dashboardSpans = "spans INDEXED BY idx_spans_dashboard"

// dashboardHTTPSpans pins the status-code query to the partial index that holds
// only spans with a status code. The WHERE clause must repeat
// "http_status IS NOT NULL" for SQLite to use it.
const dashboardHTTPSpans = "spans INDEXED BY idx_spans_dashboard_http"

// DashboardOtherGroup labels the series that folds every group outside the top N.
const DashboardOtherGroup = "other"

// DashboardGroup names a dimension a dashboard query can group by.
type DashboardGroup string

const (
	DashboardGroupService    DashboardGroup = "service"
	DashboardGroupName       DashboardGroup = "name"
	DashboardGroupHTTPStatus DashboardGroup = "http_status"
)

// groupExpr maps a dimension to its SQL expression. It is a closed set, so the
// expression is never built from request input.
func (g DashboardGroup) groupExpr() (string, bool) {
	switch g {
	case DashboardGroupService:
		return "service", true
	case DashboardGroupName:
		return "name", true
	case DashboardGroupHTTPStatus:
		return "CAST(http_status AS TEXT)", true
	}
	return "", false
}

// DashboardCountPoint is the number of spans of one group in one time bucket.
type DashboardCountPoint struct {
	Bucket time.Time
	Group  string
	Count  int64
}

// DashboardPercentilePoint carries exact percentiles of one group in one bucket.
type DashboardPercentilePoint struct {
	Bucket time.Time
	Group  string
	Count  int64
	P90Us  int64
	P95Us  int64
	P99Us  int64
}

// DashboardHeatmapCell is the number of spans whose duration falls in one
// duration bucket during one time bucket.
type DashboardHeatmapCell struct {
	Bucket         time.Time
	DurationBucket int
	Count          int64
}

// HeatmapBucketIndexSQL is the SQL expression that assigns a span to a
// half-octave duration bucket. HeatmapBucketLowerUs is its inverse. Both live
// here so the API and the frontend share one definition of the bucket edges.
const HeatmapBucketIndexSQL = "CAST(log2(duration_us + 1) * 2 AS INTEGER)"

// HeatmapBucketLowerUs returns the inclusive lower duration edge, in
// microseconds, of heatmap bucket idx.
func HeatmapBucketLowerUs(idx int) int64 {
	if idx <= 0 {
		return 0
	}
	return int64(math.Round(math.Pow(2, float64(idx)/2))) - 1
}

// dashboardWhere builds the shared predicate: the common span filters plus the
// optional root-only restriction.
func dashboardWhere(f SpanFilter, extra ...string) (string, []any) {
	var where []string
	var args []any
	where, args = f.appendCommonWhere(where, args)
	if f.RootOnly {
		where = append(where, "COALESCE(parent_span_id,'') = ''")
	}
	where = append(where, extra...)
	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// ingestedEpochSQL converts ingested_at to Unix seconds. The column holds Go's
// time.String() form ("2026-10-01 14:42:41 +0000 UTC"), which strftime cannot
// parse: it returns NULL for the whole value. The first 19 characters are always
// a valid UTC "YYYY-MM-DD HH:MM:SS", so the zone suffix and any fractional
// seconds are cut off before parsing.
const ingestedEpochSQL = "CAST(strftime('%s', substr(ingested_at, 1, 19)) AS INTEGER)"

func bucketExpr(intervalSec int64) string {
	return fmt.Sprintf("(%s / %d) * %d", ingestedEpochSQL, intervalSec, intervalSec)
}

// QueryDashboardCounts counts spans per time bucket and group. Only the topN
// groups by volume keep their own series; the rest fold into DashboardOtherGroup.
// Spans without an HTTP status code are skipped when grouping by http_status.
func (r *Repository) QueryDashboardCounts(f SpanFilter, intervalSec int64, group DashboardGroup, topN int) ([]DashboardCountPoint, error) {
	expr, ok := group.groupExpr()
	if !ok {
		return nil, fmt.Errorf("unsupported dashboard group %q", group)
	}
	var extra []string
	table := dashboardSpans
	if group == DashboardGroupHTTPStatus {
		extra = append(extra, "http_status IS NOT NULL")
		table = dashboardHTTPSpans
	}
	where, args := dashboardWhere(f, extra...)
	q := fmt.Sprintf(`SELECT %s AS bucket, %s AS grp, COUNT(*) FROM %s%s GROUP BY bucket, grp ORDER BY bucket`,
		bucketExpr(intervalSec), expr, table, where)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []DashboardCountPoint
	for rows.Next() {
		var sec, n int64
		var grp string
		if err := rows.Scan(&sec, &grp, &n); err != nil {
			return nil, err
		}
		points = append(points, DashboardCountPoint{Bucket: time.Unix(sec, 0).UTC(), Group: grp, Count: n})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return foldCountPoints(points, topN), nil
}

// foldCountPoints keeps the topN groups by total count and merges the others
// into one DashboardOtherGroup series. Output stays ordered by bucket.
func foldCountPoints(points []DashboardCountPoint, topN int) []DashboardCountPoint {
	totals := map[string]int64{}
	for _, p := range points {
		totals[p.Group] += p.Count
	}
	if topN <= 0 || len(totals) <= topN {
		return points
	}
	keep := topGroups(totals, topN)

	type key struct {
		bucket time.Time
		group  string
	}
	merged := map[key]int64{}
	var order []key
	for _, p := range points {
		g := p.Group
		if !keep[g] {
			g = DashboardOtherGroup
		}
		k := key{p.Bucket, g}
		if _, seen := merged[k]; !seen {
			order = append(order, k)
		}
		merged[k] += p.Count
	}
	out := make([]DashboardCountPoint, 0, len(order))
	for _, k := range order {
		out = append(out, DashboardCountPoint{Bucket: k.bucket, Group: k.group, Count: merged[k]})
	}
	return out
}

// topGroups returns the n groups with the highest totals, ties broken by name
// so the result is stable.
func topGroups(totals map[string]int64, n int) map[string]bool {
	names := make([]string, 0, len(totals))
	for g := range totals {
		names = append(names, g)
	}
	sort.Slice(names, func(i, j int) bool {
		if totals[names[i]] != totals[names[j]] {
			return totals[names[i]] > totals[names[j]]
		}
		return names[i] < names[j]
	})
	if n > len(names) {
		n = len(names)
	}
	keep := make(map[string]bool, n)
	for _, g := range names[:n] {
		keep[g] = true
	}
	return keep
}

// QueryDashboardPercentiles computes exact P90/P95/P99 per time bucket for the
// topN groups by span count. Percentiles do not merge, so groups outside the
// top N are omitted instead of folded into an "other" series. Percentiles are
// computed from the raw durations, never by averaging stored aggregate values.
func (r *Repository) QueryDashboardPercentiles(f SpanFilter, intervalSec int64, group DashboardGroup, topN int) ([]DashboardPercentilePoint, error) {
	expr, ok := group.groupExpr()
	if !ok || group == DashboardGroupHTTPStatus {
		return nil, fmt.Errorf("unsupported percentile group %q", group)
	}
	where, args := dashboardWhere(f)

	keep, err := r.topGroupsByCount(expr, where, args, topN)
	if err != nil {
		return nil, err
	}
	if len(keep) == 0 {
		return nil, nil
	}

	q := fmt.Sprintf(`SELECT %s, %s, duration_us FROM `+dashboardSpans+`%s LIMIT %d`,
		bucketExpr(intervalSec), expr, where, dashboardRowCap)
	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		sec   int64
		group string
	}
	durations := map[key][]int64{}
	for rows.Next() {
		var sec, dur int64
		var grp string
		if err := rows.Scan(&sec, &grp, &dur); err != nil {
			return nil, err
		}
		if !keep[grp] {
			continue
		}
		k := key{sec, grp}
		durations[k] = append(durations[k], dur)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	points := make([]DashboardPercentilePoint, 0, len(durations))
	for k, d := range durations {
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		points = append(points, DashboardPercentilePoint{
			Bucket: time.Unix(k.sec, 0).UTC(),
			Group:  k.group,
			Count:  int64(len(d)),
			P90Us:  percentileFromSorted(d, 90),
			P95Us:  percentileFromSorted(d, 95),
			P99Us:  percentileFromSorted(d, 99),
		})
	}
	sort.Slice(points, func(i, j int) bool {
		if !points[i].Bucket.Equal(points[j].Bucket) {
			return points[i].Bucket.Before(points[j].Bucket)
		}
		return points[i].Group < points[j].Group
	})
	return points, nil
}

// topGroupsByCount returns the topN values of the group expression by span count.
func (r *Repository) topGroupsByCount(expr, where string, args []any, topN int) (map[string]bool, error) {
	if topN <= 0 {
		topN = 10
	}
	q := fmt.Sprintf(`SELECT %s AS grp, COUNT(*) AS n FROM `+dashboardSpans+`%s GROUP BY grp ORDER BY n DESC, grp LIMIT %d`,
		expr, where, topN)
	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keep := map[string]bool{}
	for rows.Next() {
		var grp string
		var n int64
		if err := rows.Scan(&grp, &n); err != nil {
			return nil, err
		}
		keep[grp] = true
	}
	return keep, rows.Err()
}

// QueryDashboardHeatmap counts spans per (time bucket, duration bucket). The
// duration bucket index follows HeatmapBucketIndexSQL.
func (r *Repository) QueryDashboardHeatmap(f SpanFilter, intervalSec int64) ([]DashboardHeatmapCell, error) {
	where, args := dashboardWhere(f)
	q := fmt.Sprintf(`SELECT %s AS bucket, %s AS dbucket, COUNT(*) FROM `+dashboardSpans+`%s GROUP BY bucket, dbucket ORDER BY bucket, dbucket`,
		bucketExpr(intervalSec), HeatmapBucketIndexSQL, where)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cells []DashboardHeatmapCell
	for rows.Next() {
		var sec, n int64
		var idx int
		if err := rows.Scan(&sec, &idx, &n); err != nil {
			return nil, err
		}
		cells = append(cells, DashboardHeatmapCell{Bucket: time.Unix(sec, 0).UTC(), DurationBucket: idx, Count: n})
	}
	return cells, rows.Err()
}
