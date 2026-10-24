package aggregation

import (
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestComputeWeightedFollowsTheWeights(t *testing.T) {
	// 17 fast traces that each stand for 1000, 5 slow error traces kept at
	// weight 1: the population is overwhelmingly fast.
	var durations, weights []int64
	for range 17 {
		durations = append(durations, 50_000)
		weights = append(weights, 1000)
	}
	for range 5 {
		durations = append(durations, 15_000_000)
		weights = append(weights, 1)
	}
	if got := ComputeWeighted(durations, weights, 50); got != 50_000 {
		t.Errorf("weighted p50 = %d, want 50000", got)
	}
	if got := ComputeWeighted(durations, weights, 99); got != 50_000 {
		t.Errorf("weighted p99 = %d, want 50000", got)
	}
	if got := Compute(durations, 50); got != 50_000 {
		// 17 of 22 are fast, so the unweighted median is fast too; the
		// distinction shows on the p95 of the retained set.
		t.Errorf("unweighted p50 = %d", got)
	}
	if got := Compute(durations, 95); got != 15_000_000 {
		t.Errorf("unweighted p95 = %d, want the slow tail", got)
	}
	if got := ComputeWeighted(durations, weights, 95); got != 50_000 {
		t.Errorf("weighted p95 = %d, want 50000", got)
	}
}

func TestComputeWeightedInterpolatesAcrossWeights(t *testing.T) {
	// Half the population is 10, half is 90.
	got := ComputeWeighted([]int64{90, 10}, []int64{5, 5}, 50)
	if got != 10 {
		t.Errorf("p50 = %d, want 10 (the lower half ends at the median)", got)
	}
	if got := ComputeWeighted([]int64{90, 10}, []int64{5, 5}, 51); got != 90 {
		t.Errorf("p51 = %d, want 90", got)
	}
}

func TestPercentilesFallBackWithoutWeights(t *testing.T) {
	durations := []int64{10, 20, 30, 40, 50}
	// A zero weight means unknown: the whole set reads unweighted.
	p50, _, _ := percentilesOf(durations, []int64{1, 1, 0, 1, 1})
	if p50 != 30 {
		t.Errorf("mixed weights p50 = %d, want the unweighted 30", p50)
	}
	p50, _, _ = percentilesOf(durations, nil)
	if p50 != 30 {
		t.Errorf("no weights p50 = %d, want 30", p50)
	}
}

// The slow error traces the sampler always keeps must not set the latency of
// an operation whose normal traffic is fast (spanbarn#367).
func TestAggregatePercentilesAreWeighted(t *testing.T) {
	start := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC).UnixMicro()
	var spans []repository.Span
	add := func(dur int64, status string, weight int) {
		spans = append(spans, repository.Span{
			ProjectID: 1, Service: "controller", Name: "list_routes", Kind: "client",
			StartTimeUs: start, DurationUs: dur, Status: status, SampleWeight: weight,
		})
	}
	for range 17 {
		add(50_000, "ok", 1000)
	}
	for range 5 {
		add(15_000_000, "error", 1)
	}

	aggs, err := NewAggregator(&mockAccWriter{}, time.Hour, slog.Default()).AggregateSpans(t.Context(), spans)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggs) != 1 || aggs[0].P50Us != 50_000 || aggs[0].P95Us != 50_000 {
		t.Fatalf("aggregator = %+v, want p50 and p95 of 50ms", aggs)
	}

	acc := NewAccumulator(&mockAccWriter{}, time.Hour, time.Minute, nil)
	for _, s := range spans {
		acc.Add(s)
	}
	rows := acc.QueryRecent(repository.AggregateFilter{ProjectID: 1})
	if len(rows) != 1 || rows[0].P50Us != 50_000 || rows[0].P95Us != 50_000 {
		t.Fatalf("accumulator = %+v, want p50 and p95 of 50ms", rows)
	}
}
