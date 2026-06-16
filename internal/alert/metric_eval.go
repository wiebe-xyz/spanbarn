package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// metricEvalWindow is how far back, per comparison-window unit (one minute, the
// default rollup bucket), the evaluator looks. The extra buckets give the
// rolling baseline something to average over.
const metricLookbackPadding = 2

// evaluateMetricAlert checks a metric_threshold alert against the rollup table.
// It reduces the matching series to one scalar per bucket using the configured
// aggregation, treats the latest bucket as the current value and the earlier
// buckets as the rolling baseline, then applies the shared trigger gate.
func (e *Evaluator) evaluateMetricAlert(ctx context.Context, a repository.Alert, now time.Time) error {
	_, span := alertTracer.Start(ctx, "alert.evaluate_metric")
	defer span.End()

	if a.MetricName == "" {
		return fmt.Errorf("metric_threshold alert %d has no metric name", a.ID)
	}

	window := a.ComparisonWindow
	if window <= 0 {
		window = 10
	}
	from := now.Add(-time.Duration(window+metricLookbackPadding) * time.Minute)

	rows, err := e.repo.QueryMetricRollups(ctx, repository.MetricRollupFilter{
		ProjectID:  a.ProjectID,
		Name:       a.MetricName,
		From:       from,
		To:         now,
		Attributes: parseLabelFilters(a.LabelFilters),
	})
	if err != nil {
		return fmt.Errorf("query metric rollups: %w", err)
	}
	if len(rows) == 0 {
		return nil // no data yet
	}

	metricType := rows[0].Type
	points := mergeRollupsByBucket(rows)
	derived := metrics.Derive(metricType, points)
	if len(derived) == 0 {
		return nil
	}

	render := metrics.RenderFor(metricType)
	scalars := make([]float64, len(derived))
	for i, d := range derived {
		scalars[i] = metricScalar(a.MetricAgg, render, d)
	}

	currentVal := scalars[len(scalars)-1]
	var avgVal float64
	hasHistory := len(scalars) > 1
	if hasHistory {
		var sum float64
		for _, v := range scalars[:len(scalars)-1] {
			sum += v
		}
		avgVal = sum / float64(len(scalars)-1)
	}

	return e.maybeTrigger(ctx, span, a, currentVal, avgVal, hasHistory, now)
}

// metricScalar reduces a derived point to the value the alert compares against.
// "p95" reads the reconstructed 95th percentile; everything else (rate/avg/last)
// reads the derived value, which Derive already computed per the metric type.
func metricScalar(agg string, render metrics.RenderKind, d metrics.DerivedPoint) float64 {
	if agg == "p95" || render == metrics.RenderPercentile {
		if d.P95 != nil {
			return *d.P95
		}
		return 0
	}
	return d.Value
}

// bucketAgg accumulates all series for one bucket while merging.
type bucketAgg struct {
	count, obs int64
	sum, last  float64
	typ        string
	histBounds []float64
	histCounts []float64
	lastExtra  string
	haveHist   bool
}

// mergeRollupsByBucket collapses all matching series into one logical series:
// one InputPoint per bucket, summing counts/sums/last/obs and merging histogram
// bucket counts. For a single-series alert this is a pass-through.
func mergeRollupsByBucket(rows []repository.MetricRollup) []metrics.InputPoint {
	byBucket := map[int64]*bucketAgg{}
	var order []int64
	for _, r := range rows {
		t := r.Bucket.UnixNano()
		g := byBucket[t]
		if g == nil {
			g = &bucketAgg{typ: r.Type}
			byBucket[t] = g
			order = append(order, t)
		}
		g.count += r.Count
		g.obs += r.ObsCount
		g.sum += r.Sum
		g.last += r.Last
		g.mergeExtra(r)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	out := make([]metrics.InputPoint, 0, len(order))
	for _, t := range order {
		g := byBucket[t]
		value := g.last
		if g.typ == "gauge" && g.count > 0 {
			value = g.sum / float64(g.count)
		}
		var extra json.RawMessage
		if g.haveHist {
			if b, err := json.Marshal(map[string]any{"bounds": g.histBounds, "counts": g.histCounts}); err == nil {
				extra = b
			}
		} else if g.lastExtra != "" {
			extra = json.RawMessage(g.lastExtra)
		}
		out = append(out, metrics.InputPoint{T: t, Value: value, Count: g.obs, Extra: extra})
	}
	return out
}

func (g *bucketAgg) mergeExtra(r repository.MetricRollup) {
	if r.Extra == "" {
		return
	}
	if r.Type != "histogram" {
		g.lastExtra = r.Extra
		return
	}
	var h struct {
		Bounds []float64 `json:"bounds"`
		Counts []float64 `json:"counts"`
	}
	if json.Unmarshal([]byte(r.Extra), &h) != nil {
		return
	}
	if !g.haveHist || len(g.histCounts) != len(h.Counts) {
		g.histBounds = append([]float64(nil), h.Bounds...)
		g.histCounts = append([]float64(nil), h.Counts...)
		g.haveHist = true
		return
	}
	for i := range h.Counts {
		g.histCounts[i] += h.Counts[i]
	}
}

// parseLabelFilters decodes the alert's stored label-filter JSON into a map.
func parseLabelFilters(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	return metrics.ParseAttributes([]byte(raw))
}
