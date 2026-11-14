package metricrollup

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

// TestMergeOpaqueDistributionDifferences: the 5-minute rows of a cumulative
// exp_histogram or summary hold running totals, so the hour holds the step from
// the carry row to the newest row.
func TestMergeOpaqueDistributionDifferences(t *testing.T) {
	for _, typ := range []string{"exp_histogram", "summary"} {
		obs := func(sum float64, n int64) func(*rowBuilder) {
			return func(b *rowBuilder) { b.row.Sum, b.row.ObsCount = sum, n }
		}
		rows := []repoRow{
			src("dur", "v1", typ, testStart, obs(200, 100)),
			src("dur", "v1", typ, testStart.Add(5*time.Minute), obs(220, 110)),
			src("dur", "v1", typ, testStart.Add(10*time.Minute), obs(240, 120)),
		}
		tail := src("dur", "v1", typ, testStart.Add(-5*time.Minute), obs(180, 90))
		out := Merge(hourTier, testStart, 7, rows, DefaultDropPolicy(), Carry{
			SourceTail: map[string]repoRow{SourceTailKey(tail): tail},
		})
		if len(out) != 1 {
			t.Fatalf("%s rows = %d, want 1", typ, len(out))
		}
		if got := out[0].ObsCount; got != 30 {
			t.Errorf("%s obs_count = %d, want 30", typ, got)
		}
		if got := out[0].Sum; got != 60 {
			t.Errorf("%s sum = %v, want 60", typ, got)
		}
	}
}

// TestLadderOpaqueDistributionKeepsObservations: running totals go in at the
// 5-minute tier and the day has to hold what the hours beneath it hold, a lower
// hour after a higher one included.
func TestLadderOpaqueDistributionKeepsObservations(t *testing.T) {
	for _, typ := range []string{"exp_histogram", "summary"} {
		repo := testRepo(t)
		start := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 0, 2)

		var rows []repository.MetricRollup
		var total int64
		for b := start; b.Before(end); b = b.Add(5 * time.Minute) {
			total += int64(b.Hour()%3 + 1)
			rows = append(rows, repository.MetricRollup{
				ProjectID: 1, Name: "dur", Type: typ, Unit: "1", Temporality: "cumulative",
				AttrFingerprint: "api", Attributes: `{"service.name":"api"}`,
				Bucket: b, Count: 1, Sum: float64(total * 2), ObsCount: total,
			})
		}
		if err := repo.UpsertMetricRollups(rows); err != nil {
			t.Fatalf("UpsertMetricRollups: %v", err)
		}
		now := end.AddDate(0, 0, 1)
		c := New(repo, Config{BucketsPerPass: 1000}, slog.New(slog.DiscardHandler))
		c.now = func() time.Time { return now }
		if err := c.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}

		// The opening row has nothing before it, so its running total stays out.
		want := total - rows[0].ObsCount
		for _, step := range []int64{tier.StepHour, tier.StepDay} {
			got, err := repo.QueryCoarseRollups(context.Background(), repository.CoarseRollupFilter{
				ProjectID: 1, Name: "dur", StepSeconds: step, From: start, To: now,
			})
			if err != nil {
				t.Fatalf("QueryCoarseRollups: %v", err)
			}
			var obs int64
			var sum float64
			for _, r := range got {
				obs += r.ObsCount
				sum += r.Sum
			}
			if obs != want {
				t.Errorf("%s step %d obs_count = %d, want %d", typ, step, obs, want)
			}
			if sum != float64(want*2) {
				t.Errorf("%s step %d sum = %v, want %v", typ, step, sum, want*2)
			}
		}
	}
}
