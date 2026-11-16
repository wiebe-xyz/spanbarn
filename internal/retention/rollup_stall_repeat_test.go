package retention

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

// A stall that goes on is reported once an hour, and its end is logged once.
func TestRollupStallReportedHourlyUntilCaughtUp(t *testing.T) {
	w, repo, buf := loggedWorker(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 5, 12, 40, 0, 0, time.UTC)
	if err := repo.SetMetricRollupWatermark(ctx, tier.Step5m, t0.Add(-5*time.Hour).Truncate(time.Hour)); err != nil {
		t.Fatal(err)
	}

	for _, at := range []time.Duration{0, 10 * time.Minute, 59 * time.Minute, 61 * time.Minute} {
		w.reportRollupStalls(ctx, t0.Add(at))
	}
	if got := stallErrors(t, buf); len(got) != 2 {
		t.Fatalf("stall reported %d times over 61 minutes, want 2: %v", len(got), got)
	}

	if err := repo.SetMetricRollupWatermark(ctx, tier.Step5m, t0.Truncate(time.Hour)); err != nil {
		t.Fatal(err)
	}
	w.reportRollupStalls(ctx, t0.Add(2*time.Hour))
	w.reportRollupStalls(ctx, t0.Add(2*time.Hour+5*time.Minute))
	var caughtUp int
	for _, l := range logLines(t, buf) {
		if l.Msg == "rollup compaction caught up" && l.Tier == "hourly" {
			caughtUp++
		}
	}
	if caughtUp != 1 {
		t.Errorf("catch-up logged %d times, want 1", caughtUp)
	}
	if got := stallErrors(t, buf); len(got) != 2 {
		t.Errorf("stall reported again after the catch-up: %v", got)
	}
}

// The threshold follows the bucket size: three hours, two days.
func TestBucketsBehind(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 40, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		through time.Time
		step    int64
		want    int
	}{
		"hour current":     {time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC), tier.StepHour, 0},
		"hour two behind":  {time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC), tier.StepHour, 2},
		"hour capped":      {time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), tier.StepHour, 3},
		"day current":      {time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), tier.StepDay, 0},
		"day one behind":   {time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), tier.StepDay, 1},
		"month one behind": {time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), tier.StepMonth, 1},
	} {
		if got := bucketsBehind(tc.through, tc.step, now, stallBuckets(tc.step)); got != tc.want {
			t.Errorf("%s: %d buckets behind, want %d", name, got, tc.want)
		}
	}
}

// A compactor turned off on purpose leaves its tiers waiting by design: that
// ladder is skipped with one info line, and the other ladder is still checked.
func TestRollupStallSkipsLadderWithCompactionOff(t *testing.T) {
	w, repo, buf := loggedWorker(t)
	w.cfg.MetricCompactionOff = true
	ctx := context.Background()
	t0 := time.Date(2026, 11, 5, 12, 40, 0, 0, time.UTC)
	stale := t0.Add(-6 * time.Hour).Truncate(time.Hour)
	if err := repo.SetMetricRollupWatermark(ctx, tier.Step5m, stale); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSpanRollupWatermark(ctx, tier.Step5m, stale); err != nil {
		t.Fatal(err)
	}

	w.reportRollupStalls(ctx, t0)
	w.reportRollupStalls(ctx, t0.Add(2*time.Hour))

	got := stallErrors(t, buf)
	if len(got) != 2 || got[0] != "span/hourly" || got[1] != "span/hourly" {
		t.Errorf("stalls reported %v, want span/hourly twice and no metric tier", got)
	}
	var skipped int
	for _, l := range logLines(t, buf) {
		if l.Msg == "rollup stall check skipped: compaction is off" {
			if l.Level != "INFO" || l.Ladder != "metric" {
				t.Errorf("skip line at %s for ladder %q, want INFO for metric", l.Level, l.Ladder)
			}
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("skip logged %d times, want 1", skipped)
	}
}
