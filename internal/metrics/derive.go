// Package metrics turns raw OTLP metric data points into render-ready series.
//
// Plotting raw stored values is wrong for most OTLP shapes: a cumulative Sum is
// an ever-rising ramp (the useful view is a per-second rate), and a Histogram's
// distribution lives in its bucket counts (the useful view is p50/p95/p99), not
// in the observation count. This package centralises that shape-aware transform
// so both the live query path and the rollup path render metrics the same way.
package metrics

import (
	"encoding/json"
	"math"
	"sort"
)

// RenderKind tells the frontend how a metric series should be drawn.
type RenderKind string

const (
	// RenderLine plots the raw value over time (gauges).
	RenderLine RenderKind = "line"
	// RenderRate plots the per-second rate of a cumulative counter (sums).
	RenderRate RenderKind = "rate"
	// RenderPercentile plots p50/p95/p99 reconstructed from a distribution
	// (histogram, exponential histogram, summary).
	RenderPercentile RenderKind = "percentile"
)

// RenderFor maps an OTLP metric type to how it should be rendered.
func RenderFor(metricType string) RenderKind {
	switch metricType {
	case "sum":
		return RenderRate
	case "histogram", "exp_histogram", "summary":
		return RenderPercentile
	default: // gauge and anything unrecognised
		return RenderLine
	}
}

// InputPoint is one raw stored data point of a metric, with its attributes
// already parsed into string labels.
type InputPoint struct {
	T          int64 // time_unix_nano
	Value      float64
	Count      int64
	Extra      json.RawMessage
	Attributes map[string]string
}

// DerivedPoint is a render-ready point. Which fields are populated depends on
// the metric's RenderKind: Value for line/rate, the P* fields for percentile.
type DerivedPoint struct {
	T     int64    `json:"t"`
	Value float64  `json:"value"`
	P50   *float64 `json:"p50,omitempty"`
	P95   *float64 `json:"p95,omitempty"`
	P99   *float64 `json:"p99,omitempty"`
	Count int64    `json:"count"`
}

// Series is one line in a chart: a label set plus its derived points.
type Series struct {
	Labels map[string]string `json:"labels"`
	Points []DerivedPoint    `json:"points"`
}

// BuildSeries splits raw points into one Series per distinct label set (or per
// distinct combination of groupBy keys when groupBy is non-empty), then derives
// render-ready points for each according to metricType. Input order is
// preserved within each series; callers should pass points sorted by time.
func BuildSeries(metricType string, in []InputPoint, groupBy []string) []Series {
	type bucket struct {
		labels map[string]string
		points []InputPoint
	}
	order := []string{}
	groups := map[string]*bucket{}

	for _, p := range in {
		key, labels := seriesKey(p.Attributes, groupBy)
		b := groups[key]
		if b == nil {
			b = &bucket{labels: labels}
			groups[key] = b
			order = append(order, key)
		}
		b.points = append(b.points, p)
	}

	out := make([]Series, 0, len(order))
	for _, key := range order {
		b := groups[key]
		out = append(out, Series{
			Labels: b.labels,
			Points: Derive(metricType, b.points),
		})
	}
	return out
}

// seriesKey computes a stable grouping key and the label set to display for a
// point. With groupBy set, only those keys participate; otherwise the full
// attribute set defines the series.
func seriesKey(attrs map[string]string, groupBy []string) (string, map[string]string) {
	labels := map[string]string{}
	if len(groupBy) > 0 {
		for _, k := range groupBy {
			if v, ok := attrs[k]; ok {
				labels[k] = v
			}
		}
	} else {
		for k, v := range attrs {
			labels[k] = v
		}
	}

	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb []byte
	for _, k := range keys {
		sb = append(sb, k...)
		sb = append(sb, '\x00')
		sb = append(sb, labels[k]...)
		sb = append(sb, '\x00')
	}
	return string(sb), labels
}

// Derive converts the raw points of a single series into render-ready points.
// points must be sorted ascending by T.
func Derive(metricType string, points []InputPoint) []DerivedPoint {
	switch RenderFor(metricType) {
	case RenderRate:
		return deriveRate(points)
	case RenderPercentile:
		return derivePercentiles(metricType, points)
	default:
		return deriveGauge(points)
	}
}

func deriveGauge(points []InputPoint) []DerivedPoint {
	out := make([]DerivedPoint, 0, len(points))
	for _, p := range points {
		out = append(out, DerivedPoint{T: p.T, Value: p.Value, Count: p.Count})
	}
	return out
}

// deriveRate converts a cumulative counter into a per-second rate. It assumes
// cumulative temporality (the OTLP default): the rate at point i is the
// increase since point i-1 divided by the elapsed seconds. A decrease is
// treated as a counter reset, so the new value itself is taken as the increase.
func deriveRate(points []InputPoint) []DerivedPoint {
	out := make([]DerivedPoint, 0, len(points))
	for i := 1; i < len(points); i++ {
		prev, cur := points[i-1], points[i]
		dtSec := float64(cur.T-prev.T) / 1e9
		if dtSec <= 0 {
			continue
		}
		delta := cur.Value - prev.Value
		if delta < 0 { // counter reset
			delta = cur.Value
		}
		out = append(out, DerivedPoint{T: cur.T, Value: delta / dtSec, Count: cur.Count})
	}
	return out
}

func derivePercentiles(metricType string, points []InputPoint) []DerivedPoint {
	out := make([]DerivedPoint, 0, len(points))
	for _, p := range points {
		var p50, p95, p99 float64
		switch metricType {
		case "histogram":
			p50, p95, p99 = histogramPercentiles(p.Extra)
		case "exp_histogram":
			p50, p95, p99 = expHistogramPercentiles(p.Extra)
		case "summary":
			p50, p95, p99 = summaryPercentiles(p.Extra)
		}
		dp := DerivedPoint{T: p.T, Count: p.Count}
		dp.P50, dp.P95, dp.P99 = &p50, &p95, &p99
		out = append(out, dp)
	}
	return out
}

// --- Histogram (explicit bucket) reconstruction ---

type histExtra struct {
	Bounds []float64 `json:"bounds"`
	Counts []float64 `json:"counts"`
}

func histogramPercentiles(extra json.RawMessage) (p50, p95, p99 float64) {
	var h histExtra
	if len(extra) == 0 || json.Unmarshal(extra, &h) != nil {
		return 0, 0, 0
	}
	return histogramQuantile(h.Bounds, h.Counts, 0.50),
		histogramQuantile(h.Bounds, h.Counts, 0.95),
		histogramQuantile(h.Bounds, h.Counts, 0.99)
}

// histogramQuantile estimates the q-quantile (0..1) of an OTLP explicit-bucket
// histogram using linear interpolation within the matching bucket, the same
// approach Prometheus uses. bounds has N entries; counts has N+1 (the final
// entry is the +Inf overflow bucket). The first bucket is assumed to start at 0.
func histogramQuantile(bounds, counts []float64, q float64) float64 {
	if len(counts) == 0 {
		return 0
	}
	var total float64
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return 0
	}

	target := q * total
	var cum float64
	for i, c := range counts {
		if cum+c < target {
			cum += c
			continue
		}
		lower := 0.0
		if i > 0 && i-1 < len(bounds) {
			lower = bounds[i-1]
		}
		// Overflow bucket (no upper bound): best estimate is its lower edge.
		if i >= len(bounds) {
			return lower
		}
		upper := bounds[i]
		if c <= 0 {
			return lower
		}
		frac := (target - cum) / c
		return lower + frac*(upper-lower)
	}
	// Numerical edge: target == total. Return the largest finite bound.
	if len(bounds) > 0 {
		return bounds[len(bounds)-1]
	}
	return 0
}

// --- Exponential histogram reconstruction (positive buckets + zero) ---

type expBuckets struct {
	Offset       int     `json:"offset"`
	BucketCounts []int64 `json:"bucket_counts"`
}

type expExtra struct {
	Scale     int        `json:"scale"`
	ZeroCount int64      `json:"zero_count"`
	Positive  expBuckets `json:"positive"`
	Negative  expBuckets `json:"negative"`
}

func expHistogramPercentiles(extra json.RawMessage) (p50, p95, p99 float64) {
	var e expExtra
	if len(extra) == 0 || json.Unmarshal(extra, &e) != nil {
		return 0, 0, 0
	}
	bounds, counts := expToExplicit(e)
	return histogramQuantile(bounds, counts, 0.50),
		histogramQuantile(bounds, counts, 0.95),
		histogramQuantile(bounds, counts, 0.99)
}

// expToExplicit flattens an exponential histogram's positive buckets and zero
// bucket into explicit (bounds, counts) form so the shared quantile routine can
// be reused. Negative buckets are uncommon for the latency/size metrics this
// targets and are not represented. The base of the scale is 2^(2^-scale); a
// positive bucket at index i covers (base^i, base^(i+1)].
func expToExplicit(e expExtra) (bounds, counts []float64) {
	base := math.Pow(2, math.Pow(2, float64(-e.Scale)))

	// Zero bucket: values at (or near) zero, upper bound ~0.
	if e.ZeroCount > 0 {
		bounds = append(bounds, 0)
		counts = append(counts, float64(e.ZeroCount))
	}
	for j, c := range e.Positive.BucketCounts {
		idx := e.Positive.Offset + j
		upper := math.Pow(base, float64(idx+1))
		bounds = append(bounds, upper)
		counts = append(counts, float64(c))
	}
	// Trailing overflow bucket expected by histogramQuantile (empty).
	counts = append(counts, 0)
	return bounds, counts
}

// --- Summary (stored quantiles) ---

type summaryQuantile struct {
	Quantile float64 `json:"quantile"`
	Value    float64 `json:"value"`
}

type summaryExtra struct {
	Quantiles []summaryQuantile `json:"quantiles"`
}

func summaryPercentiles(extra json.RawMessage) (p50, p95, p99 float64) {
	var s summaryExtra
	if len(extra) == 0 || json.Unmarshal(extra, &s) != nil {
		return 0, 0, 0
	}
	return nearestQuantile(s.Quantiles, 0.50),
		nearestQuantile(s.Quantiles, 0.95),
		nearestQuantile(s.Quantiles, 0.99)
}

// nearestQuantile returns the value of the stored quantile closest to q.
func nearestQuantile(qs []summaryQuantile, q float64) float64 {
	best := 0.0
	bestDist := math.MaxFloat64
	for _, sq := range qs {
		if d := math.Abs(sq.Quantile - q); d < bestDist {
			bestDist = d
			best = sq.Value
		}
	}
	return best
}
