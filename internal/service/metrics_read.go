package service

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

// rollupQueryThreshold is the range width above which a series read uses
// pre-aggregated rollups rather than raw data points.
const rollupQueryThreshold = 6 * time.Hour

// FilteredFineTierWidth is the widest range a label filter reads from the
// 5-minute tier. The filter has to look at every row in the range unless enough
// of them match to reach the limit, so a selective filter costs far more than an
// unfiltered read, and a two-day read of one times out where the hour tier answers
// in under a second.
const FilteredFineTierWidth = 24 * time.Hour

// MetricInputRequest is one metric read: which series, over which range, and
// whether the caller wants a total over the range rather than a chart.
type MetricInputRequest struct {
	ProjectID int64
	Name      string
	From, To  time.Time
	Labels    map[string]string
	Limit     int
	// Totals marks a sum over the range, which reads the hour tier from a day on.
	Totals bool
}

// MetricInput is the derivation input of one read. Step is the resolution it
// was read at in seconds: 0 for raw data points, otherwise the tier's width.
type MetricInput struct {
	Type, Unit string
	Points     []metrics.InputPoint
	Step       int64
}

// ReadInput reads raw points for a narrow range and rollups for a long one,
// which spares a long range the raw scan. A narrow range with no raw points also
// reads rollups: raw points expire long before the tiers do, and an empty result
// there would look like a metric that reported nothing. The returned step is 0
// for raw points.
func (s *MetricsService) ReadInput(ctx context.Context, req MetricInputRequest) (MetricInput, error) {
	if req.To.Sub(req.From) > rollupQueryThreshold {
		return s.rollupInput(ctx, req)
	}
	in, err := s.rawInput(ctx, req)
	if err != nil || len(in.Points) > 0 {
		return in, err
	}
	return s.rollupInput(ctx, req)
}

// rawInput loads raw metric data points and maps them to derivation input.
func (s *MetricsService) rawInput(ctx context.Context, req MetricInputRequest) (MetricInput, error) {
	rows, err := s.repo.QueryMetricSeries(ctx, MetricFilter{
		ProjectID: req.ProjectID, Name: req.Name, From: req.From, To: req.To, Attributes: req.Labels, Limit: req.Limit,
	})
	if err != nil {
		return MetricInput{}, err
	}
	var out MetricInput
	out.Points = make([]metrics.InputPoint, 0, len(rows))
	for _, row := range rows {
		if out.Type == "" {
			out.Type, out.Unit = row.Type, row.Unit
		}
		p := metrics.InputPoint{
			T:          row.TimeUnixNano,
			Value:      row.Value,
			Count:      row.Count,
			Extra:      MarshalMetricExtra(row),
			Attributes: metrics.ParseAttributes([]byte(row.Attributes)),
		}
		if row.DeltaSum() {
			p.Delta = true
			p.Extra = nil
			if row.StartTimeUnixNano > 0 && row.TimeUnixNano > row.StartTimeUnixNano {
				p.IntervalNano = row.TimeUnixNano - row.StartTimeUnixNano
			}
		}
		out.Points = append(out.Points, p)
	}
	return out, nil
}

// rollupTierSteps lists the resolutions worth trying for a range, finest first.
//
// The finest tier that still gives a readable chart is preferred, and the
// coarser ones are fallbacks: the fine tiers are kept for days while the coarse
// ones go back years, so a month-old week-long range simply has no 5-minute
// buckets left to read. Asking in order turns that into a slightly smoother
// chart instead of an empty one.
func rollupTierSteps(width time.Duration) []int64 {
	switch {
	case width <= 48*time.Hour:
		return []int64{tier.Step5m, tier.StepHour, tier.StepDay}
	case width <= 30*24*time.Hour:
		return []int64{tier.StepHour, tier.StepDay, tier.StepWeek}
	case width <= 365*24*time.Hour:
		return []int64{tier.StepDay, tier.StepWeek, tier.StepMonth}
	default:
		return []int64{tier.StepWeek, tier.StepMonth}
	}
}

// tierStepsForRead is rollupTierSteps with the 5-minute tier left out of a
// filtered read too wide for it. Falling back to the whole range of the 5-minute
// tier would turn every filter that matches nothing into a timeout, so the hour
// tier read takes only its uncompacted tail from there (hourTierWithTail).
//
// A totals read starts at the hour tier from a day on, filtered or not. It sums
// every bucket in the range and has no chart to keep smooth, so the 12 times
// fewer rows are all gain: a delta total is the same sum either way, and a
// cumulative one only loses a reset that happened inside one hour.
func tierStepsForRead(width time.Duration, labels map[string]string, totals bool) []int64 {
	steps := rollupTierSteps(width)
	wide := len(labels) > 0 && width > FilteredFineTierWidth
	if totals && width >= FilteredFineTierWidth {
		wide = true
	}
	if wide && steps[0] == tier.Step5m {
		return steps[1:]
	}
	return steps
}

// rollupInput loads downsampled rollup buckets and maps them to derivation
// input, returning the resolution it read. The per-type value choice keeps
// metrics.Derive working unchanged: gauges use the bucket average, counters use
// the bucket-end cumulative value (so rate is derived across buckets),
// distributions carry their merged extra. Each read starts at the bucket holding
// from, so a range narrower than a coarse bucket still finds that bucket.
func (s *MetricsService) rollupInput(ctx context.Context, req MetricInputRequest) (MetricInput, error) {
	filter := MetricRollupFilter{
		ProjectID: req.ProjectID, Name: req.Name, From: req.From, To: req.To, Attributes: req.Labels, Limit: req.Limit,
	}
	fineRead := false
	for _, step := range tierStepsForRead(req.To.Sub(req.From), req.Labels, req.Totals) {
		filter.From = tier.TruncateBucket(req.From, step)
		var rows []MetricRollup
		var err error
		// A read that has tried the 5-minute tier and found nothing has no
		// tail to add.
		if step == tier.StepHour && !fineRead {
			rows, err = s.hourTierWithTail(ctx, filter, req.From)
		} else {
			rows, err = s.queryTier(ctx, step, filter)
		}
		fineRead = fineRead || step == tier.Step5m
		if err != nil {
			return MetricInput{}, err
		}
		if len(rows) == 0 {
			continue
		}
		return rollupRowsInput(rows, step), nil
	}
	return MetricInput{}, nil
}

// rollupRowsInput maps rollup buckets read at step to derivation input. A delta
// bucket's interval is its own width, or step when the row carries none.
func rollupRowsInput(rows []MetricRollup, step int64) MetricInput {
	out := MetricInput{Step: step, Points: make([]metrics.InputPoint, 0, len(rows))}
	for _, row := range rows {
		if out.Type == "" {
			out.Type, out.Unit = row.Type, row.Unit
		}
		p := rollupToInput(row)
		if p.Delta {
			width := row.StepSeconds
			if width == 0 {
				width = step
			}
			p.IntervalNano = width * int64(time.Second)
		}
		out.Points = append(out.Points, p)
	}
	return out
}

// queryTier reads one resolution. The 5-minute tier is the accumulator's own
// table; every coarser tier shares metric_rollups_coarse.
func (s *MetricsService) queryTier(ctx context.Context, step int64, f MetricRollupFilter) ([]MetricRollup, error) {
	if step == Rollup5mStep {
		return s.Rollups(ctx, f)
	}
	return s.CoarseRollups(ctx, CoarseRollupFilter{
		ProjectID:   f.ProjectID,
		Name:        f.Name,
		StepSeconds: step,
		From:        f.From,
		To:          f.To,
		Attributes:  f.Attributes,
		Limit:       f.Limit,
	})
}

// rollupToInput maps a rollup bucket to a derivation input point with its
// attributes, which the series path groups by.
func rollupToInput(row MetricRollup) metrics.InputPoint {
	p := RollupInputPoint(row)
	p.Attributes = metrics.ParseAttributes([]byte(row.Attributes))
	return p
}
