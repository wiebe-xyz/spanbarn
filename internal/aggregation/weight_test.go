package aggregation

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// weightSpans is one bucket of three spans: two from a trace sampled 1 in 100
// and one of unknown weight.
func weightSpans() []repository.Span {
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC).UnixMicro()
	s := repository.Span{ProjectID: 1, Service: "api", Name: "GET /", StartTimeUs: start, DurationUs: 10, SampleWeight: 100}
	unknown := s
	unknown.SampleWeight = 0
	return []repository.Span{s, s, unknown}
}

func checkWeights(t *testing.T, where string, aggs []repository.Aggregate) {
	t.Helper()
	if len(aggs) != 1 {
		t.Fatalf("%s: %d aggregates, want 1", where, len(aggs))
	}
	a := aggs[0]
	if a.Count != 3 || a.WeightedCount != 200 || a.WeightedSpans != 2 {
		t.Errorf("%s: count %d, weighted_count %d, weighted_spans %d, want 3, 200, 2", where, a.Count, a.WeightedCount, a.WeightedSpans)
	}
}

// TestAggregationSumsWeights checks every path that turns spans into
// aggregate rows: an unknown weight counts toward Count only, so the row
// reports itself as partly weighted.
func TestAggregationSumsWeights(t *testing.T) {
	ctx := context.Background()
	spans := weightSpans()

	aggs, err := NewAggregator(&mockAccWriter{}, time.Minute, slog.Default()).AggregateSpans(ctx, spans)
	if err != nil {
		t.Fatal(err)
	}
	checkWeights(t, "Aggregator.AggregateSpans", aggs)

	w := &mockAccWriter{}
	acc := NewAccumulator(w, time.Minute, time.Hour, nil)
	aggs, err = acc.AggregateSpans(ctx, spans)
	if err != nil {
		t.Fatal(err)
	}
	checkWeights(t, "Accumulator.AggregateSpans", aggs)

	for _, s := range spans {
		acc.Add(s)
	}
	checkWeights(t, "Accumulator.QueryRecent", acc.QueryRecent(repository.AggregateFilter{}))
	if err := acc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	checkWeights(t, "Accumulator.Flush", w.get())
}
