package retention

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

func insertSpanRollup(t *testing.T, repo *repository.Repository, step int64, bucket time.Time) {
	t.Helper()
	var h spanhist.Hist
	h.Observe(10)
	if err := repo.AddSpanRollups([]repository.SpanRollup{{
		StepSeconds: step, Bucket: bucket.UTC().Truncate(time.Duration(step) * time.Second),
		ProjectID: 1, Service: "api", Name: "op", Kind: "server", Status: "ok",
		Count: 1, SumDurationUs: 10, MinUs: 10, MaxUs: 10, Hist: h.Encode(),
	}}); err != nil {
		t.Fatalf("AddSpanRollups: %v", err)
	}
}

func countSpanRollups(t *testing.T, repo *repository.Repository, step int64) int {
	t.Helper()
	var n int
	if err := repo.DB().QueryRow(`SELECT COUNT(*) FROM span_rollups WHERE step_seconds = ?`, step).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A tier that was never compacted keeps every row, however old.
func TestSpanRollupTierKeptUntilCompacted(t *testing.T) {
	w, repo := setupTestWorker(t, Config{})
	now := time.Now().UTC()
	insertSpanRollup(t, repo, 300, now.Add(-10*24*time.Hour))
	insertSpanRollup(t, repo, 3600, now.Add(-90*24*time.Hour))

	n, more, err := w.deleteSpanRollupTiers(context.Background(), w.cfg, now, maxRollupRowsPerCycle)
	if err != nil || n != 0 || more {
		t.Fatalf("deleted %d (more=%v, err=%v), want 0", n, more, err)
	}
	if countSpanRollups(t, repo, 300) != 1 || countSpanRollups(t, repo, 3600) != 1 {
		t.Error("uncompacted span rollups were deleted")
	}
}

// Deletion stops at the earlier of the window and the watermark, and the
// daily tier goes by its window alone.
func TestSpanRollupTierStopsAtWatermark(t *testing.T) {
	w, repo := setupTestWorker(t, Config{SpanRollup5mHours: 72, SpanRollupDailyDays: 400})
	ctx := context.Background()
	now := time.Now().UTC()
	insertSpanRollup(t, repo, 300, now.Add(-100*time.Hour)) // past window, before watermark
	insertSpanRollup(t, repo, 300, now.Add(-80*time.Hour))  // past window, after watermark
	insertSpanRollup(t, repo, 300, now.Add(-time.Hour))     // inside window
	insertSpanRollup(t, repo, 86400, now.Add(-500*24*time.Hour))
	insertSpanRollup(t, repo, 86400, now.Add(-24*time.Hour))
	if err := repo.SetSpanRollupWatermark(ctx, 300, now.Add(-90*time.Hour)); err != nil {
		t.Fatal(err)
	}

	n, more, err := w.deleteSpanRollupTiers(ctx, w.cfg, now, maxRollupRowsPerCycle)
	if err != nil || n != 2 || more {
		t.Fatalf("deleted %d (more=%v, err=%v), want 2", n, more, err)
	}
	if got := countSpanRollups(t, repo, 300); got != 2 {
		t.Errorf("5m rows left = %d, want 2", got)
	}
	if got := countSpanRollups(t, repo, 86400); got != 1 {
		t.Errorf("daily rows left = %d, want 1", got)
	}
}

// A budget smaller than the backlog leaves the rest for the next cycle.
func TestSpanRollupTierRespectsBudget(t *testing.T) {
	w, repo := setupTestWorker(t, Config{})
	ctx := context.Background()
	now := time.Now().UTC()
	for i := range 3 {
		insertSpanRollup(t, repo, 300, now.Add(-time.Duration(100+i)*time.Hour))
	}
	if err := repo.SetSpanRollupWatermark(ctx, 300, now); err != nil {
		t.Fatal(err)
	}
	n, more, err := w.deleteSpanRollupTiers(ctx, w.cfg, now, 2)
	if err != nil || n != 2 || !more {
		t.Fatalf("deleted %d (more=%v, err=%v), want 2 with backlog", n, more, err)
	}
	if n, more, _ := w.deleteSpanRollupTiers(ctx, w.cfg, now, 0); n != 0 || !more {
		t.Errorf("zero budget: deleted %d, more=%v", n, more)
	}
}

// The cycle trims span rollups alongside metric rollups and counts them.
func TestCycleTrimsSpanRollups(t *testing.T) {
	w, repo := setupTestWorker(t, Config{})
	ctx := context.Background()
	now := time.Now().UTC()
	insertSpanRollup(t, repo, 300, now.Add(-200*time.Hour))
	if err := repo.SetSpanRollupWatermark(ctx, 300, now); err != nil {
		t.Fatal(err)
	}
	var st cycleStats
	if err := w.deleteAllRollupTiers(ctx, w.cfg, now, maxRollupRowsPerCycle, &st); err != nil {
		t.Fatal(err)
	}
	if st.spanRollupRowsDeleted != 1 || st.rollupBacklogRemains {
		t.Errorf("stats = %+v", st)
	}
}

func TestPressureShortensSpanRollup5mTier(t *testing.T) {
	cfg := Config{}.withDefaults()
	got := TierCritical.Apply(cfg)
	if got.SpanRollup5mHours >= cfg.SpanRollup5mHours || got.SpanRollup5mHours < 6 {
		t.Errorf("critical 5m span window = %dh from %dh", got.SpanRollup5mHours, cfg.SpanRollup5mHours)
	}
	if got.SpanRollupHourlyDays != cfg.SpanRollupHourlyDays || got.SpanRollupDailyDays != cfg.SpanRollupDailyDays {
		t.Error("pressure must leave the coarse span rollup tiers alone")
	}
}
