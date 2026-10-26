package repository

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// Dashboard queries read raw spans over a short window (retention keeps ~48h)
// and bucket on ingested_at, the same server-authoritative key the span indexes
// and retention use.

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
	DashboardGroupStatus     DashboardGroup = "status"
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
	case DashboardGroupStatus:
		return "status", true
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
// optional root-only restriction and maximum duration.
func dashboardWhere(f SpanFilter, extra ...string) (string, []any) {
	var where []string
	var args []any
	where, args = f.appendCommonWhere(where, args)
	if f.RootOnly {
		where = append(where, "COALESCE(parent_span_id,'') = ''")
	}
	if f.MaxDuration > 0 {
		where = append(where, "duration_us <= ?")
		args = append(args, f.MaxDuration)
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

// bucketKey groups spans on a prefix of ingested_at instead of computing Unix
// seconds per row. strftime costs about as much as the index scan itself on the
// 0.3 CPU staging pod, and a text prefix groups just as well. SQL groups on the
// prefix (minute, or second below one minute); Go folds prefixes into buckets.
type bucketKey struct {
	sql      string
	layout   string
	interval int64
}

func newBucketKey(intervalSec int64) bucketKey {
	if intervalSec >= 60 && intervalSec%60 == 0 {
		return bucketKey{sql: "substr(ingested_at, 1, 16)", layout: "2006-01-02 15:04", interval: intervalSec}
	}
	return bucketKey{sql: "substr(ingested_at, 1, 19)", layout: "2006-01-02 15:04:05", interval: intervalSec}
}

// epoch parses a prefix produced by b.sql and floors it to the bucket width.
func (b bucketKey) epoch(prefix string) (int64, error) {
	t, err := time.Parse(b.layout, prefix)
	if err != nil {
		return 0, fmt.Errorf("parse ingested_at prefix %q: %w", prefix, err)
	}
	sec := t.Unix()
	return sec - ((sec%b.interval)+b.interval)%b.interval, nil
}

// bucketMemo remembers the last prefix parsed. Rows arrive in index order, so
// consecutive rows share a prefix and nearly every lookup is a string compare.
type bucketMemo struct {
	key    bucketKey
	prefix string
	sec    int64
	ok     bool
}

func (m *bucketMemo) epoch(prefix string) (int64, error) {
	if m.ok && prefix == m.prefix {
		return m.sec, nil
	}
	sec, err := m.key.epoch(prefix)
	if err != nil {
		return 0, err
	}
	m.prefix, m.sec, m.ok = prefix, sec, true
	return sec, nil
}

// QueryDashboardCounts counts spans per time bucket and group. Only the topN
// groups by volume keep their own series; the rest fold into DashboardOtherGroup.
// Spans without an HTTP status code are skipped when grouping by http_status.
func (r *Repository) QueryDashboardCounts(ctx context.Context, f SpanFilter, intervalSec int64, group DashboardGroup, topN int) ([]DashboardCountPoint, error) {
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
	bk := newBucketKey(intervalSec)
	q := fmt.Sprintf(`SELECT %s AS bucket, %s AS grp, COUNT(*) FROM %s%s GROUP BY bucket, grp ORDER BY bucket`,
		bk.sql, expr, table, where)

	ctx, cancel := r.readContext(ctx)
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
	totals := map[key]int64{}
	for rows.Next() {
		var prefix, grp string
		var n int64
		if err := rows.Scan(&prefix, &grp, &n); err != nil {
			return nil, err
		}
		sec, err := bk.epoch(prefix)
		if err != nil {
			return nil, err
		}
		totals[key{sec, grp}] += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	points := make([]DashboardCountPoint, 0, len(totals))
	for k, n := range totals {
		points = append(points, DashboardCountPoint{Bucket: time.Unix(k.sec, 0).UTC(), Group: k.group, Count: n})
	}
	sort.Slice(points, func(i, j int) bool {
		if !points[i].Bucket.Equal(points[j].Bucket) {
			return points[i].Bucket.Before(points[j].Bucket)
		}
		return points[i].Group < points[j].Group
	})
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

// QueryDashboardPercentiles computes P90/P95/P99 per time bucket for the topN
// groups by span count. SQLite folds the spans into quarter-octave duration
// bins per bucket and group (see internal/spanhist), so a percentile lands
// inside the observed range of its bin: exact for evenly spaced durations and
// within the bin's 19% width otherwise. Percentiles do not merge across
// groups, so groups outside the top N are omitted instead of folded into an
// "other" series.
func (r *Repository) QueryDashboardPercentiles(ctx context.Context, f SpanFilter, intervalSec int64, group DashboardGroup, topN int) ([]DashboardPercentilePoint, error) {
	expr, ok := group.groupExpr()
	if !ok || group == DashboardGroupHTTPStatus {
		return nil, fmt.Errorf("unsupported percentile group %q", group)
	}
	where, args := dashboardWhere(f)

	bk := newBucketKey(intervalSec)
	q := fmt.Sprintf(`SELECT %s AS bucket, %s AS grp, %s AS bin, COUNT(*), MIN(duration_us), MAX(duration_us)
		FROM `+dashboardSpans+`%s GROUP BY bucket, grp, bin`,
		bk.sql, expr, spanhist.IndexSQL("duration_us"), where)
	ctx, cancel := r.readContext(ctx)
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// One pass collects every group; the top N by span count is chosen
	// afterwards, so no separate counting scan is needed.
	type key struct {
		sec   int64
		group string
	}
	hists := map[key]*spanhist.Hist{}
	totals := map[string]int64{}
	memo := bucketMemo{key: bk}
	for rows.Next() {
		var prefix, grp string
		var b spanhist.Bin
		if err := rows.Scan(&prefix, &grp, &b.Index, &b.Count, &b.Min, &b.Max); err != nil {
			return nil, err
		}
		sec, err := memo.epoch(prefix)
		if err != nil {
			return nil, err
		}
		k := key{sec, grp}
		h := hists[k]
		if h == nil {
			h = &spanhist.Hist{}
			hists[k] = h
		}
		h.Add(b)
		totals[grp] += b.Count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if topN <= 0 {
		topN = 10
	}
	keep := topGroups(totals, topN)
	points := make([]DashboardPercentilePoint, 0, len(hists))
	for k, h := range hists {
		if !keep[k.group] {
			continue
		}
		points = append(points, DashboardPercentilePoint{
			Bucket: time.Unix(k.sec, 0).UTC(),
			Group:  k.group,
			Count:  h.Count(),
			P90Us:  h.Percentile(90),
			P95Us:  h.Percentile(95),
			P99Us:  h.Percentile(99),
		})
	}
	if len(points) == 0 {
		return nil, nil
	}
	sort.Slice(points, func(i, j int) bool {
		if !points[i].Bucket.Equal(points[j].Bucket) {
			return points[i].Bucket.Before(points[j].Bucket)
		}
		return points[i].Group < points[j].Group
	})
	return points, nil
}

// QueryDashboardHeatmap counts spans per (time bucket, duration bucket). The
// duration bucket index follows HeatmapBucketIndexSQL.
func (r *Repository) QueryDashboardHeatmap(ctx context.Context, f SpanFilter, intervalSec int64) ([]DashboardHeatmapCell, error) {
	where, args := dashboardWhere(f)
	bk := newBucketKey(intervalSec)
	q := fmt.Sprintf(`SELECT %s AS bucket, %s AS dbucket, COUNT(*) FROM `+dashboardSpans+`%s GROUP BY bucket, dbucket`,
		bk.sql, HeatmapBucketIndexSQL, where)

	ctx, cancel := r.readContext(ctx)
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		sec int64
		idx int
	}
	totals := map[key]int64{}
	for rows.Next() {
		var prefix string
		var idx int
		var n int64
		if err := rows.Scan(&prefix, &idx, &n); err != nil {
			return nil, err
		}
		sec, err := bk.epoch(prefix)
		if err != nil {
			return nil, err
		}
		totals[key{sec, idx}] += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cells := make([]DashboardHeatmapCell, 0, len(totals))
	for k, n := range totals {
		cells = append(cells, DashboardHeatmapCell{Bucket: time.Unix(k.sec, 0).UTC(), DurationBucket: k.idx, Count: n})
	}
	sort.Slice(cells, func(i, j int) bool {
		if !cells[i].Bucket.Equal(cells[j].Bucket) {
			return cells[i].Bucket.Before(cells[j].Bucket)
		}
		return cells[i].DurationBucket < cells[j].DurationBucket
	})
	return cells, nil
}
