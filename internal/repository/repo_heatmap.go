package repository

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

// HeatmapWindow bounds a duration heatmap scan. Expr selects the spans and may
// be nil for every span of the project in the range.
type HeatmapWindow struct {
	ProjectID int64
	From, To  time.Time
	Expr      *filter.Expr
	// MaxSpans caps how many spans are read, newest first.
	MaxSpans        int
	TimeBuckets     int
	DurationBuckets int
}

// HeatmapCell is the number of spans in one time bucket and duration bucket.
type HeatmapCell struct {
	Time     int
	Duration int
	Count    int64
}

// HeatmapScan is the bucketed duration distribution of one span set.
type HeatmapScan struct {
	// Scanned is the number of spans read.
	Scanned int64
	// BucketMicros is the width of one time bucket. Bucket i starts at
	// From + i*BucketMicros.
	BucketMicros int64
	// DurationEdgesUs has DurationBuckets+1 ascending log-scale edges. Bucket i
	// holds durations in [edges[i], edges[i+1]); the last bucket includes its
	// upper edge. Empty when no span was read.
	DurationEdgesUs []int64
	Cells           []HeatmapCell
}

// durationEdges returns n+1 ascending edges spaced evenly on a log scale from lo
// to hi. lo is raised to 1 so the logarithm is defined, and hi is kept above lo.
func durationEdges(lo, hi int64, n int) []int64 {
	lo = max(lo, 1)
	hi = max(hi, lo+1)
	edges := make([]int64, n+1)
	ratio := float64(hi) / float64(lo)
	for i := range edges {
		edges[i] = int64(math.Round(float64(lo) * math.Pow(ratio, float64(i)/float64(n))))
	}
	edges[0], edges[n] = lo, hi
	return edges
}

// durationBucketSQL renders the CASE expression that maps duration_us to its
// bucket index. The edges are integers computed by durationEdges, never user
// text, so they are written into the statement.
func durationBucketSQL(edges []int64) string {
	var b strings.Builder
	b.WriteString("CASE")
	for i := 1; i < len(edges)-1; i++ {
		fmt.Fprintf(&b, " WHEN duration_us < %d THEN %d", edges[i], i-1)
	}
	fmt.Fprintf(&b, " ELSE %d END", len(edges)-2)
	return b.String()
}

func heatmapCTE(w HeatmapWindow) (string, []any, error) {
	if w.From.IsZero() {
		return "", nil, fmt.Errorf("%w: a time range (from) is required", filter.ErrInvalid)
	}
	pred, predArgs, err := filter.Compile(w.Expr)
	if err != nil {
		return "", nil, err
	}
	q := `s AS (
		SELECT start_time_us, duration_us FROM spans
		WHERE project_id = ? AND ingested_at >= ? AND ingested_at <= ?`
	args := []any{w.ProjectID, w.From, w.To}
	if pred != "" {
		q += ` AND (` + pred + `)`
		args = append(args, predArgs...)
	}
	q += ` ORDER BY ingested_at DESC LIMIT ?)`
	return q, append(args, w.MaxSpans), nil
}

// ScanHeatmap counts spans per time bucket and log-scale duration bucket. The
// time axis is the span start time, so a rectangle on the grid maps back to a
// start_time_us and duration_us filter. Spans that started before From fall in
// the first time bucket.
func (r *Repository) ScanHeatmap(ctx context.Context, w HeatmapWindow) (*HeatmapScan, error) {
	if w.TimeBuckets < 1 || w.DurationBuckets < 1 {
		return nil, fmt.Errorf("%w: bucket counts must be positive", filter.ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	cte, args, err := heatmapCTE(w)
	if err != nil {
		return nil, err
	}
	var lo, hi int64
	scan := &HeatmapScan{}
	err = r.db.QueryRowContext(ctx,
		`WITH `+cte+` SELECT COUNT(*), COALESCE(MIN(duration_us), 0), COALESCE(MAX(duration_us), 0) FROM s`, args...,
	).Scan(&scan.Scanned, &lo, &hi)
	if err != nil {
		return nil, err
	}
	if scan.Scanned == 0 {
		return scan, nil
	}

	totalUs := w.To.Sub(w.From).Microseconds()
	scan.BucketMicros = max((totalUs+int64(w.TimeBuckets)-1)/int64(w.TimeBuckets), 1)
	scan.DurationEdgesUs = durationEdges(lo, hi, w.DurationBuckets)

	q := `WITH ` + cte + `
		SELECT tb, db, COUNT(*) FROM (
			SELECT MAX(0, MIN(?, (start_time_us - ?) / ?)) AS tb, ` + durationBucketSQL(scan.DurationEdgesUs) + ` AS db FROM s
		) GROUP BY tb, db ORDER BY tb, db`
	args = append(args, w.TimeBuckets-1, w.From.UnixMicro(), scan.BucketMicros)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c HeatmapCell
		if err := rows.Scan(&c.Time, &c.Duration, &c.Count); err != nil {
			return nil, err
		}
		scan.Cells = append(scan.Cells, c)
	}
	return scan, rows.Err()
}
