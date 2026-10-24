package repository

import (
	"testing"
	"time"
)

func TestWeightedPercentile(t *testing.T) {
	var durations, weights []int64
	for range 17 {
		durations = append(durations, 50_000)
		weights = append(weights, 1000)
	}
	for range 5 {
		durations = append(durations, 15_000_000)
		weights = append(weights, 1)
	}
	for _, pct := range []float64{50, 95, 99} {
		if got := WeightedPercentile(durations, weights, pct); got != 50_000 {
			t.Errorf("p%v = %d, want 50000", pct, got)
		}
	}
	if WeightedPercentile(nil, nil, 50) != 0 || WeightedPercentile([]int64{1}, nil, 50) != 0 {
		t.Error("empty or mismatched input must read 0")
	}
	if FullyWeighted([]int64{1, 0}) || FullyWeighted(nil) || !FullyWeighted([]int64{1, 2}) {
		t.Error("FullyWeighted must be false for an unknown weight or no spans")
	}
}

// Slow error traces the sampler always keeps must not set the latency of an
// operation whose normal traffic is fast (spanbarn#367). Spans without a
// weight keep the unweighted percentiles.
func TestSpanStatsPercentilesAreWeighted(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	var spans []Span
	for i := range 17 {
		s := makeSpan(p.ID, "ok"+string(rune('a'+i)), "s", "svc", "list_routes", "ok", 50_000)
		s.SampleWeight = 1000
		spans = append(spans, s)
	}
	for i := range 5 {
		s := makeSpan(p.ID, "err"+string(rune('a'+i)), "s", "svc", "list_routes", "error", 15_000_000)
		s.SampleWeight = 1
		spans = append(spans, s)
	}
	legacy := makeSpan(p.ID, "old1", "s", "svc", "legacy_op", "ok", 100)
	legacy2 := makeSpan(p.ID, "old2", "s", "svc", "legacy_op", "ok", 900)
	spans = append(spans, legacy, legacy2)
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	ops, err := repo.QueryOperationStatsFromSpans(p.ID, "svc", time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]OperationStats{}
	for _, o := range ops {
		byName[o.Operation] = o
	}
	if got := byName["list_routes"]; got.P50Us != 50_000 || got.P95Us != 50_000 {
		t.Errorf("list_routes p50/p95 = %d/%d, want 50000/50000", got.P50Us, got.P95Us)
	}
	if got := byName["legacy_op"]; got.P50Us != 100 {
		t.Errorf("legacy_op p50 = %d, want the unweighted 100", got.P50Us)
	}

	svcs, err := repo.QueryServiceStatsFromSpans(p.ID, time.Time{}, time.Time{}, "")
	if err != nil || len(svcs) != 1 {
		t.Fatalf("service stats = %+v, %v", svcs, err)
	}
	// The service mixes weighted and unweighted spans, so it reads unweighted.
	if svcs[0].P50Us == 0 {
		t.Error("service p50 must still be computed")
	}
}
