package service

import (
	"context"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// SourceAggregates labels a read answered from the per-minute aggregates,
// whose counts are scaled by each operation's sample rate.
const SourceAggregates = "aggregates"

// allOperations is the operation path value that asks for a service's
// timeseries over every operation.
const allOperations = "*"

// rollupFold sums span rollup rows into one summary.
type rollupFold struct {
	errorCount, effective int64
	hist                  spanhist.Hist
}

func (f *rollupFold) add(s repository.SpanRollup, rate float64) {
	var errors int64
	if s.Status == "error" {
		errors = s.Count
	}
	f.errorCount += errors
	f.effective += estimateSpans(s.Count, errors, repository.SpanWeights{Count: s.WeightedCount, Spans: s.WeightedSpans}, rate)
	if h, err := spanhist.Decode(s.Hist); err == nil {
		f.hist.Merge(h)
	}
}

func (f *rollupFold) errorRate() float64 {
	if f.effective == 0 {
		return 0
	}
	return float64(f.errorCount) / float64(f.effective)
}

// foldSpanRollups folds the rows q selects into one summary per key. Each row
// is estimated by the sample rate of its own operation, as aggregates are.
func foldSpanRollups[K comparable](ctx context.Context, s *QueryService, projectID int64, q repository.SpanRollupScan, key func(repository.SpanRollup) K) (map[K]*rollupFold, error) {
	rates := s.newOpRates(ctx, projectID)
	out := map[K]*rollupFold{}
	err := s.repo.ScanSpanRollups(ctx, q, func(r repository.SpanRollup) error {
		k := key(r)
		f := out[k]
		if f == nil {
			f = &rollupFold{}
			out[k] = f
		}
		f.add(r, rates.rate(r.Name))
		return nil
	})
	return out, err
}

// servicesFromRollups returns the per-service summaries from span_rollups.
// ok is false when they cannot answer: the window is under an hour, or the
// rollups do not hold all of it.
func (s *QueryService) servicesFromRollups(ctx context.Context, projectID int64, from, to time.Time, kind string) ([]ServiceSummary, bool, error) {
	if from.IsZero() || to.IsZero() || to.Sub(from) < traceGroupRollupMinWindow {
		return nil, false, nil
	}
	segs, ok := s.spanRollupPlan(ctx, from, to, 0)
	if !ok {
		return nil, false, nil
	}
	byService, err := foldSpanRollups(ctx, s, projectID, repository.SpanRollupScan{Segments: segs, ProjectID: projectID, Kind: kind},
		func(r repository.SpanRollup) string { return r.Service })
	if err != nil {
		return nil, true, err
	}
	out := make([]ServiceSummary, 0, len(byService))
	for svc, f := range byService {
		out = append(out, ServiceSummary{
			Service: svc, SpanCount: f.effective, ErrorCount: f.errorCount, ErrorRate: f.errorRate(),
			P50Us: f.hist.Percentile(50), P95Us: f.hist.Percentile(95), P99Us: f.hist.Percentile(99),
			Source: SourceRollups,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SpanCount != out[j].SpanCount {
			return out[i].SpanCount > out[j].SpanCount
		}
		return out[i].Service < out[j].Service
	})
	return out, true, nil
}

// timeseriesFromRollups returns an operation's timeseries from span_rollups,
// or every operation of the service for allOperations. ok is false when the
// interval is not a multiple of 5 minutes or the rollups do not hold the
// whole range.
func (s *QueryService) timeseriesFromRollups(ctx context.Context, projectID int64, svcName, operation string, from, to time.Time, interval time.Duration) ([]TimeseriesBucket, bool, error) {
	sec := int64(interval / time.Second)
	if from.IsZero() || to.IsZero() || interval%time.Second != 0 {
		return nil, false, nil
	}
	segs, ok := s.spanRollupPlan(ctx, from, to, sec)
	if !ok {
		return nil, false, nil
	}
	name := operation
	if name == allOperations {
		name = ""
	}
	byBucket, err := foldSpanRollups(ctx, s, projectID, repository.SpanRollupScan{Segments: segs, ProjectID: projectID, Service: svcName, Name: name},
		func(r repository.SpanRollup) int64 { b := r.Bucket.Unix(); return b - b%sec })
	if err != nil {
		return nil, true, err
	}
	out := make([]TimeseriesBucket, 0, len(byBucket))
	for b, f := range byBucket {
		out = append(out, TimeseriesBucket{
			Bucket: time.Unix(b, 0).UTC(), Count: f.effective, ErrorCount: f.errorCount,
			P50Us: f.hist.Percentile(50), P95Us: f.hist.Percentile(95), P99Us: f.hist.Percentile(99),
			Source: SourceRollups,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket.Before(out[j].Bucket) })
	return out, true, nil
}

// timeseriesInterval is the chart bucket width for a timeseries request that
// names none: the dashboard's width for the same window.
func timeseriesInterval(from, to time.Time, interval time.Duration) time.Duration {
	if interval > 0 {
		return interval
	}
	if from.IsZero() || to.IsZero() {
		return time.Minute
	}
	// Aggregates are per minute, so a finer chart bucket would show gaps.
	return max(time.Duration(dashboardInterval(to.Sub(from)))*time.Second, time.Minute)
}
