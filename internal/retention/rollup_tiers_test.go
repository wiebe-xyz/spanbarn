package retention

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/rollup"
)

func insertFineRollup(t *testing.T, repo *repository.Repository, bucket time.Time) {
	t.Helper()
	if err := repo.UpsertMetricRollups([]repository.MetricRollup{{
		ProjectID: 1, Name: "m", Type: "gauge", Unit: "1",
		AttrFingerprint: "fp", Attributes: "{}", Bucket: bucket.UTC(),
		Count: 1, Sum: 1,
	}}); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}
}

func insertCoarseRollup(t *testing.T, repo *repository.Repository, step int64, bucket time.Time) {
	t.Helper()
	if err := repo.UpsertCoarseRollups(context.Background(), []repository.MetricRollup{{
		ProjectID: 1, Name: "m", Type: "gauge", Unit: "1",
		AttrFingerprint: "fp", Attributes: "{}", StepSeconds: step, Bucket: bucket.UTC(),
		Count: 1, Sum: 1,
	}}); err != nil {
		t.Fatalf("UpsertCoarseRollups: %v", err)
	}
}

func countRows(t *testing.T, repo *repository.Repository, table string) int {
	t.Helper()
	var n int
	if err := repo.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestRollupTierKeptUntilCompacted is the guarantee that lets the 5-minute tier
// have a two-day window at all: nothing is deleted before the compactor has
// summarised it. If compaction stalls, data waits instead of disappearing.
func TestRollupTierKeptUntilCompacted(t *testing.T) {
	w, repo := setupTestWorker(t, Config{MetricRollupRetentionDays: 2})
	now := time.Now().UTC()
	insertFineRollup(t, repo, now.Add(-10*24*time.Hour))

	deleted, _, err := w.deleteRollupTiers(context.Background(), w.cfg, now, maxRollupRowsPerCycle)
	if err != nil {
		t.Fatalf("deleteRollupTiers: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted %d rows with no compaction watermark, want 0", deleted)
	}
	if got := countRows(t, repo, "metric_rollups"); got != 1 {
		t.Errorf("rows remaining = %d, want 1", got)
	}
}

// TestRollupTierDeletesOnlyUpToWatermark: the window says two days, but only the
// part that has been compacted may go.
func TestRollupTierDeletesOnlyUpToWatermark(t *testing.T) {
	w, repo := setupTestWorker(t, Config{MetricRollupRetentionDays: 2})
	ctx := context.Background()
	now := time.Now().UTC()

	insertFineRollup(t, repo, now.Add(-10*24*time.Hour)) // compacted, past the window
	insertFineRollup(t, repo, now.Add(-3*24*time.Hour))  // past the window, NOT compacted
	if err := repo.SetMetricRollupWatermark(ctx, rollup.Step5m, now.Add(-5*24*time.Hour)); err != nil {
		t.Fatalf("SetMetricRollupWatermark: %v", err)
	}

	deleted, _, err := w.deleteRollupTiers(ctx, w.cfg, now, maxRollupRowsPerCycle)
	if err != nil {
		t.Fatalf("deleteRollupTiers: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d rows, want 1 (only what sits before the watermark)", deleted)
	}
	if got := countRows(t, repo, "metric_rollups"); got != 1 {
		t.Errorf("rows remaining = %d, want 1", got)
	}
}

// TestRollupTierWindowWinsWhenWatermarkIsAhead: once compaction has caught up,
// the configured window is what decides.
func TestRollupTierWindowWinsWhenWatermarkIsAhead(t *testing.T) {
	w, repo := setupTestWorker(t, Config{MetricRollupRetentionDays: 2})
	ctx := context.Background()
	now := time.Now().UTC()

	insertFineRollup(t, repo, now.Add(-3*24*time.Hour)) // past the window
	insertFineRollup(t, repo, now.Add(-1*time.Hour))    // inside the window
	if err := repo.SetMetricRollupWatermark(ctx, rollup.Step5m, now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("SetMetricRollupWatermark: %v", err)
	}

	deleted, _, err := w.deleteRollupTiers(ctx, w.cfg, now, maxRollupRowsPerCycle)
	if err != nil {
		t.Fatalf("deleteRollupTiers: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d rows, want 1", deleted)
	}
	if got := countRows(t, repo, "metric_rollups"); got != 1 {
		t.Errorf("rows remaining = %d, want 1 (the recent bucket)", got)
	}
}

// TestCoarsestTierNeedsNoWatermark: nothing compacts the monthly tier further,
// so waiting for a watermark would keep it forever.
func TestCoarsestTierNeedsNoWatermark(t *testing.T) {
	w, repo := setupTestWorker(t, Config{MonthlyRollupDays: 30})
	now := time.Now().UTC()
	insertCoarseRollup(t, repo, rollup.StepMonth, now.AddDate(0, -6, 0))

	deleted, _, err := w.deleteRollupTiers(context.Background(), w.cfg, now, maxRollupRowsPerCycle)
	if err != nil {
		t.Fatalf("deleteRollupTiers: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d rows, want 1", deleted)
	}
}

// TestMonthlyTierKeptIndefinitelyByDefault: 0 days means keep, which is the
// point of having a monthly tier at all.
func TestMonthlyTierKeptIndefinitelyByDefault(t *testing.T) {
	w, repo := setupTestWorker(t, Config{})
	now := time.Now().UTC()
	insertCoarseRollup(t, repo, rollup.StepMonth, now.AddDate(-5, 0, 0))

	deleted, _, err := w.deleteRollupTiers(context.Background(), w.cfg, now, maxRollupRowsPerCycle)
	if err != nil {
		t.Fatalf("deleteRollupTiers: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted %d monthly rows, want 0", deleted)
	}
	if got := countRows(t, repo, "metric_rollups_coarse"); got != 1 {
		t.Errorf("rows remaining = %d, want 1", got)
	}
}

// TestPressureShortensOnlyTheFineTier: disk pressure gives up resolution, never
// the compacted history — the coarse tiers are what an operator reads after the
// incident that caused the pressure.
func TestPressureShortensOnlyTheFineTier(t *testing.T) {
	cfg := Config{
		MetricRollupRetentionDays: 8,
		HourlyRollupDays:          30,
		DailyRollupDays:           365,
		WeeklyRollupDays:          730,
		MonthlyRollupDays:         0,
	}

	critical := TierCritical.Apply(cfg)
	if critical.MetricRollupRetentionDays != 2 {
		t.Errorf("5m tier under critical pressure = %d days, want 2", critical.MetricRollupRetentionDays)
	}
	if critical.HourlyRollupDays != 30 || critical.DailyRollupDays != 365 ||
		critical.WeeklyRollupDays != 730 || critical.MonthlyRollupDays != 0 {
		t.Errorf("coarse tiers were shortened by pressure: %+v", critical)
	}

	if got := TierCritical.Apply(Config{MetricRollupRetentionDays: 1}).MetricRollupRetentionDays; got != 1 {
		t.Errorf("5m tier floor = %d, want 1 day", got)
	}
}

// TestRollupBudgetCapsOneCycleAndResumes: the first cycle after compaction
// starts faces a backlog of millions of rows. Draining it in one call is what
// took production's retention worker out of service for twelve minutes — no
// deletes of anything else, no disk re-measurement, no log line. A cycle now
// stops at its budget, says a backlog remains, and the next cycle picks up.
func TestRollupBudgetCapsOneCycleAndResumes(t *testing.T) {
	w, repo := setupTestWorker(t, Config{MetricRollupRetentionDays: 2})
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 40; i++ {
		insertFineRollup(t, repo, now.Add(-time.Duration(10*24+i)*time.Hour))
	}
	if err := repo.SetMetricRollupWatermark(ctx, rollup.Step5m, now); err != nil {
		t.Fatalf("SetMetricRollupWatermark: %v", err)
	}

	deleted, backlog, err := w.deleteRollupTiers(ctx, w.cfg, now, 10)
	if err != nil {
		t.Fatalf("deleteRollupTiers: %v", err)
	}
	if deleted != 10 {
		t.Errorf("deleted %d rows, want the 10 the budget allows", deleted)
	}
	if !backlog {
		t.Error("a cycle that stopped at its budget must report the backlog it left")
	}
	if got := countRows(t, repo, "metric_rollups"); got != 30 {
		t.Errorf("rows remaining = %d, want 30", got)
	}

	deleted, backlog, err = w.deleteRollupTiers(ctx, w.cfg, now, maxRollupRowsPerCycle)
	if err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if deleted != 30 {
		t.Errorf("second cycle deleted %d rows, want the remaining 30", deleted)
	}
	if backlog {
		t.Error("nothing is left, so no backlog should be reported")
	}
}
