package repository

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// The dashboard reads span_rollups for windows whose chart buckets are whole
// 5-minute buckets. Rollups count every span the writer received, where raw
// spans hold only what was stored, so these results report received spans:
// a span with a sample weight counts for its weight and one without counts
// once (SpanRollup.Received).

// DashboardRollupQuery is a dashboard read from span_rollups.
type DashboardRollupQuery struct {
	Scan SpanRollupScan
	// Interval is the chart bucket width in seconds, a multiple of every
	// segment's step.
	Interval int64
	// BinLo and BinHi keep the quarter-octave bins [BinLo, BinHi) of each
	// histogram, a duration band on heatmap bucket edges. BinHi 0 keeps every
	// bin from BinLo up.
	BinLo, BinHi int
}

func (q DashboardRollupQuery) banded() bool { return q.BinLo > 0 || q.BinHi > 0 }

// band returns the part of the row's spans inside the duration band, and the
// received spans that part stands for. A histogram that does not decode
// counts as empty, as it does in compaction, so one bad row cannot fail
// every read of its window.
func (q DashboardRollupQuery) band(s SpanRollup) (spanhist.Hist, float64) {
	h, err := spanhist.Decode(s.Hist)
	if err != nil {
		return spanhist.Hist{}, 0
	}
	hi := q.BinHi
	if hi <= 0 {
		hi = spanhist.Bins
	}
	in := h.Between(q.BinLo, hi)
	if s.Count == 0 {
		return in, 0
	}
	return in, float64(s.Received()) * float64(in.Count()) / float64(s.Count)
}

func (q DashboardRollupQuery) chartBucket(s SpanRollup) int64 {
	sec := s.Bucket.Unix()
	return sec - sec%q.Interval
}

// Received is the span population the row stands for: spans that carried a
// sample weight count for it, spans without one count once.
func (s SpanRollup) Received() int64 { return s.Count - s.WeightedSpans + s.WeightedCount }

// rollupGroup returns the row's value of a dashboard group. ok is false for
// a row without an HTTP status code when grouping by it.
func rollupGroup(g DashboardGroup, s SpanRollup) (string, bool) {
	switch g {
	case DashboardGroupService:
		return s.Service, true
	case DashboardGroupName:
		return s.Name, true
	case DashboardGroupStatus:
		return s.Status, true
	case DashboardGroupHTTPStatus:
		return strconv.FormatInt(s.HTTPStatus, 10), s.HTTPStatus > 0
	}
	return "", false
}

type rollupPointKey struct {
	sec   int64
	group string
}

// QueryDashboardCountsFromRollups is QueryDashboardCounts over span_rollups,
// counting received spans. Without a duration band SQLite sums the counts and
// the histograms are not read.
func (r *Repository) QueryDashboardCountsFromRollups(ctx context.Context, q DashboardRollupQuery, group DashboardGroup, topN int) ([]DashboardCountPoint, error) {
	if _, ok := group.groupExpr(); !ok {
		return nil, fmt.Errorf("unsupported dashboard group %q", group)
	}
	totals := map[rollupPointKey]float64{}
	var err error
	if q.banded() {
		err = r.ScanSpanRollups(ctx, q.Scan, func(s SpanRollup) error {
			g, ok := rollupGroup(group, s)
			if !ok {
				return nil
			}
			if in, received := q.band(s); in.Count() > 0 {
				totals[rollupPointKey{q.chartBucket(s), g}] += received
			}
			return nil
		})
	} else {
		err = r.sumSpanRollups(ctx, q, group, totals)
	}
	if err != nil {
		return nil, err
	}
	points := make([]DashboardCountPoint, 0, len(totals))
	for k, n := range totals {
		points = append(points, DashboardCountPoint{Bucket: time.Unix(k.sec, 0).UTC(), Group: k.group, Count: int64(math.Round(n))})
	}
	sort.Slice(points, func(i, j int) bool {
		if !points[i].Bucket.Equal(points[j].Bucket) {
			return points[i].Bucket.Before(points[j].Bucket)
		}
		return points[i].Group < points[j].Group
	})
	return foldCountPoints(points, topN), nil
}

// sumSpanRollups adds the received spans per chart bucket and group into
// totals, summed in SQL. Grouping by http_status skips rows without a code.
func (r *Repository) sumSpanRollups(ctx context.Context, q DashboardRollupQuery, group DashboardGroup, totals map[rollupPointKey]float64) error {
	col := string(group)
	ctx, cancel := r.readContext(ctx)
	defer cancel()
	for _, seg := range q.Scan.Segments {
		if q.Interval%seg.Step != 0 {
			return fmt.Errorf("interval %d is not a multiple of step %d", q.Interval, seg.Step)
		}
		where, args := q.Scan.where(seg)
		if group == DashboardGroupHTTPStatus {
			where = append(where, "http_status > 0")
		}
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT (bucket / %d) * %d AS b, CAST(%s AS TEXT) AS grp,
			SUM(count - weighted_spans + weighted_count)
			FROM span_rollups WHERE %s GROUP BY b, grp`, q.Interval, q.Interval, col, strings.Join(where, " AND ")), args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k rollupPointKey
			var n int64
			if err := rows.Scan(&k.sec, &k.group, &n); err != nil {
				rows.Close()
				return err
			}
			totals[k] += float64(n)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// QueryDashboardPercentilesFromRollups is QueryDashboardPercentiles over
// span_rollups: the histograms of each chart bucket and group merge, and the
// groups are ranked by received spans.
func (r *Repository) QueryDashboardPercentilesFromRollups(ctx context.Context, q DashboardRollupQuery, group DashboardGroup, topN int) ([]DashboardPercentilePoint, error) {
	if _, ok := group.groupExpr(); !ok || group == DashboardGroupHTTPStatus {
		return nil, fmt.Errorf("unsupported percentile group %q", group)
	}
	hists := map[rollupPointKey]*spanhist.Hist{}
	received := map[rollupPointKey]float64{}
	totals := map[string]int64{}
	err := r.ScanSpanRollups(ctx, q.Scan, func(s SpanRollup) error {
		g, _ := rollupGroup(group, s)
		in, n := q.band(s)
		if in.Count() == 0 {
			return nil
		}
		k := rollupPointKey{q.chartBucket(s), g}
		h := hists[k]
		if h == nil {
			h = &spanhist.Hist{}
			hists[k] = h
		}
		h.Merge(in)
		received[k] += n
		totals[g] += int64(math.Round(n))
		return nil
	})
	if err != nil {
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
			Bucket: time.Unix(k.sec, 0).UTC(), Group: k.group, Count: int64(math.Round(received[k])),
			P90Us: h.Percentile(90), P95Us: h.Percentile(95), P99Us: h.Percentile(99),
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

// QueryDashboardHeatmapFromRollups is QueryDashboardHeatmap over
// span_rollups. A heatmap bucket is two quarter-octave bins, and each row's
// bins are scaled to the received spans the row stands for.
func (r *Repository) QueryDashboardHeatmapFromRollups(ctx context.Context, q DashboardRollupQuery) ([]DashboardHeatmapCell, error) {
	type key struct {
		sec int64
		idx int
	}
	totals := map[key]float64{}
	err := r.ScanSpanRollups(ctx, q.Scan, func(s SpanRollup) error {
		in, n := q.band(s)
		if in.Count() == 0 {
			return nil
		}
		scale := n / float64(in.Count())
		sec := q.chartBucket(s)
		for _, b := range in.Bins() {
			totals[key{sec, b.Index >> 1}] += float64(b.Count) * scale
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	cells := make([]DashboardHeatmapCell, 0, len(totals))
	for k, n := range totals {
		if c := int64(math.Round(n)); c > 0 {
			cells = append(cells, DashboardHeatmapCell{Bucket: time.Unix(k.sec, 0).UTC(), DurationBucket: k.idx, Count: c})
		}
	}
	sort.Slice(cells, func(i, j int) bool {
		if !cells[i].Bucket.Equal(cells[j].Bucket) {
			return cells[i].Bucket.Before(cells[j].Bucket)
		}
		return cells[i].DurationBucket < cells[j].DurationBucket
	})
	return cells, nil
}
