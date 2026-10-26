package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/rollup"
)

const (
	// maxInsights caps how many notable changes are returned.
	maxInsights = 20
	// insightsCoarseFrom is the window width from which insights read the
	// hourly tier: 144 five-minute buckets per series and up are where the
	// 5-minute read stopped fitting the read budget.
	insightsCoarseFrom = 12 * time.Hour
	// insightsRecentShare is the part of the window that is compared with the
	// rest, its baseline.
	insightsRecentShare = 0.3
)

// insightsGrain snaps the window: the 5-minute tier changes once per bucket,
// the hourly tier once per compaction pass.
func insightsGrain(window time.Duration) time.Duration {
	if window >= insightsCoarseFrom {
		return 15 * time.Minute
	}
	return 5 * time.Minute
}

// Insights scans every metric series of a project in [from, to] and returns
// the most notable changes (spikes, drops, percentile regressions and new
// series), largest first. The last insightsRecentShare of the window is
// compared with the rest.
func (s *MetricsService) Insights(ctx context.Context, projectID int64, from, to time.Time) ([]metrics.Insight, error) {
	grain := insightsGrain(to.Sub(from))
	from, to = from.Truncate(grain), to.Truncate(grain)
	key := fmt.Sprintf("insights:%d:%d:%d", projectID, from.Unix(), to.Unix())
	return readCached(ctx, s.cache, key, grain, func(ctx context.Context) ([]metrics.Insight, error) {
		step, end, err := s.insightsTier(ctx, from, to)
		if err != nil {
			return nil, err
		}
		d := insightDetector{split: from.Add(time.Duration(float64(end.Sub(from)) * (1 - insightsRecentShare))).UnixNano()}
		if err := s.repo.ScanProjectRollups(ctx, projectID, step, from, end, d.add); err != nil {
			return nil, err
		}
		return d.result(), nil
	})
}

// insightsTier picks the tier a window reads and where that read ends. A
// window of insightsCoarseFrom or more reads the hourly tier up to its
// compaction watermark, the end of the last hour compacted. The 5-minute tier
// is the fallback when nothing is compacted inside the window, or when the
// watermark lags so far that the recent part of the window would be missing.
func (s *MetricsService) insightsTier(ctx context.Context, from, to time.Time) (int64, time.Time, error) {
	window := to.Sub(from)
	if window < insightsCoarseFrom {
		return rollup.Step5m, to, nil
	}
	through, ok, err := s.repo.MetricRollupWatermark(ctx, rollup.Step5m)
	if err != nil {
		return 0, time.Time{}, err
	}
	if !ok || to.Sub(through) > time.Duration(float64(window)*insightsRecentShare) {
		return rollup.Step5m, to, nil
	}
	// Buckets start at their hour: the last complete one starts before through.
	return rollup.StepHour, minTime(to, through.Add(-time.Second)), nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// insightDetector runs change detection on each series as the scan streams
// it. Rows arrive ordered by name, fingerprint and bucket, so a series is one
// contiguous run and only one is held at a time.
type insightDetector struct {
	split    int64
	name     string
	typ      string
	attrs    string
	fp       string
	pts      []metrics.InputPoint
	insights []metrics.Insight
}

func (d *insightDetector) add(row MetricRollup) error {
	if row.Name != d.name || row.AttrFingerprint != d.fp {
		d.flush()
		d.name, d.typ, d.attrs, d.fp = row.Name, row.Type, row.Attributes, row.AttrFingerprint
		d.pts = d.pts[:0]
	}
	d.pts = append(d.pts, RollupInputPoint(row))
	return nil
}

func (d *insightDetector) flush() {
	if len(d.pts) == 0 {
		return
	}
	if in, ok := metrics.DetectSeries(d.name, d.typ, metrics.ParseAttributes([]byte(d.attrs)), d.pts, d.split); ok {
		d.insights = append(d.insights, in)
	}
}

// result flushes the last series and returns the maxInsights largest changes.
func (d *insightDetector) result() []metrics.Insight {
	d.flush()
	out := d.insights
	sort.SliceStable(out, func(i, j int) bool { return out[i].Magnitude() > out[j].Magnitude() })
	if len(out) > maxInsights {
		out = out[:maxInsights]
	}
	if out == nil {
		out = []metrics.Insight{}
	}
	return out
}

// RollupInputPoint maps a rollup bucket to a derivation input point, without
// its attributes. The per-type value choice keeps metrics.Derive working
// unchanged: gauges use the bucket average, counters use the bucket-end
// cumulative value (so rate is derived across buckets), distributions carry
// their merged extra. A delta sum has no running total to difference: its
// bucket sum is the increase, and the caller sets the interval it covers.
func RollupInputPoint(row MetricRollup) metrics.InputPoint {
	value := row.Last
	if row.Type == "gauge" && row.Count > 0 {
		value = row.Sum / float64(row.Count)
	}
	delta := row.Type == "sum" && row.Temporality == rollup.TemporalityDelta
	if delta {
		value = row.Sum
	}
	return metrics.InputPoint{
		T:     row.Bucket.UnixNano(),
		Value: value,
		Count: row.ObsCount,
		Extra: json.RawMessage(row.Extra),
		Delta: delta,
	}
}
