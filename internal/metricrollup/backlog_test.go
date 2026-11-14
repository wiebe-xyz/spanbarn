package metricrollup

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

func tierRows(t *testing.T, repo *repository.Repository, step int64, from, to time.Time) []repository.MetricRollup {
	t.Helper()
	got, err := repo.QueryCoarseRollups(context.Background(), repository.CoarseRollupFilter{
		ProjectID: 1, Name: "reqs", StepSeconds: step, From: from, To: to,
	})
	if err != nil {
		t.Fatalf("QueryCoarseRollups(%d): %v", step, err)
	}
	return got
}

// TestCompactorDrainsBacklogTierByTier: two weeks of a counter wait while each
// pass may write twelve buckets per tier. Every day and week has to hold all of
// its source rows once the backlog is gone. The series' first day and week open
// on its first sample, so the checks start one bucket later.
func TestCompactorDrainsBacklogTierByTier(t *testing.T) {
	repo := testRepo(t)
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	end := monday.AddDate(0, 0, 14)

	var samples []repository.MetricRollup
	var total float64
	for b := monday; b.Before(end); b = b.Add(5 * time.Minute) {
		total++
		samples = append(samples, counterRow(b, "v1", total))
	}
	if err := repo.UpsertMetricRollups(samples); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}

	c := New(repo, Config{BucketsPerPass: 12}, slog.New(slog.DiscardHandler))
	now := end.AddDate(0, 0, 1)
	c.now = func() time.Time { return now }
	for range 40 {
		if err := c.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
	}

	days := tierRows(t, repo, tier.StepDay, monday, end)
	if len(days) != 14 {
		t.Fatalf("days = %d, want 14", len(days))
	}
	for _, d := range days[1:] {
		if d.Sum != 288 {
			t.Errorf("day %s increase = %v, want 288", d.Bucket.Format("01-02"), d.Sum)
		}
	}
	weeks := tierRows(t, repo, tier.StepWeek, monday, end)
	if len(weeks) != 2 {
		t.Fatalf("weeks = %d, want 2", len(weeks))
	}
	if weeks[1].Sum != 2016 {
		t.Errorf("second week increase = %v, want 2016", weeks[1].Sum)
	}
}

// TestCompactorWaitsForSourceTier: a closed day stays open to compaction, with its
// watermark unmoved, until the hours beneath it are done.
func TestCompactorWaitsForSourceTier(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)

	var samples []repository.MetricRollup
	for i := range 288 {
		samples = append(samples, counterRow(day.Add(time.Duration(i)*5*time.Minute), "v1", float64(i)))
	}
	if err := repo.UpsertMetricRollups(samples); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}

	c := New(repo, Config{BucketsPerPass: 12}, slog.New(slog.DiscardHandler))
	now := day.AddDate(0, 0, 2)
	c.now = func() time.Time { return now }
	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := tierRows(t, repo, tier.StepDay, day, now); len(got) != 0 {
		t.Errorf("day rows after half the hours = %d, want 0", len(got))
	}
	if _, ok, err := repo.MetricRollupWatermark(ctx, tier.StepHour); err != nil || ok {
		t.Errorf("day tier watermark set = %v (err %v), want unset", ok, err)
	}
}

// TestCompactorRunsWithoutSourceWatermark: hour rows with no 5-minute watermark
// have nothing on the way to them, and waiting would stop compaction and with it
// retention of that tier.
func TestCompactorRunsWithoutSourceWatermark(t *testing.T) {
	repo := testRepo(t)
	day := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)

	hour := counterRow(day.Add(time.Hour), "v1", 40)
	hour.Sum, hour.StepSeconds = 40, tier.StepHour
	if err := repo.UpsertCoarseRollups(context.Background(), []repository.MetricRollup{hour}); err != nil {
		t.Fatalf("UpsertCoarseRollups: %v", err)
	}

	now := day.AddDate(0, 0, 2)
	if err := newTestCompactor(t, repo, now).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := tierRows(t, repo, tier.StepDay, day, now); len(got) != 1 {
		t.Errorf("day rows = %d, want 1", len(got))
	}
}
