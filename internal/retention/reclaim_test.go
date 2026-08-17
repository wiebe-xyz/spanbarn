package retention

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestReclaimToTargetEvictsUntilItFits is the point of the whole change. The
// tiering in pressure.go multiplies a duration; this loop measures a size,
// evicts, and measures again. With a target the volume cannot meet, it must
// keep halving the window down to the floor rather than stopping after one
// pass — that is the difference between "we tried" and "we bounded it".
func TestReclaimToTargetEvictsUntilItFits(t *testing.T) {
	cfg := Config{
		InterestingRetentionHours: 48,
		ErrorRetentionDays:        30,
		ErrorLogRetentionDays:     30,
		AggregateRetentionDays:    365,
		SlowThresholdUS:           1_000_000,
		Watermarks:                Watermarks{Elevated: 0.0001, Critical: 0.0002},
		// Unreachable on a real volume, so the loop runs to its floor.
		TargetFraction: 0.0003,
	}
	worker, repo := setupDiskWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// Spread spans across ages that survive successive halvings of a 48h
	// window: 40h, 20h, 10h, 5h, 2h, 1h.
	for _, age := range []time.Duration{40, 20, 10, 5, 2, 1} {
		insertTestSpans(t, repo, 2, "ok", 500, time.Now().UTC().Add(-age*time.Hour))
	}

	var before int
	if err := repo.DB().QueryRow("SELECT COUNT(*) FROM spans").Scan(&before); err != nil {
		t.Fatalf("count: %v", err)
	}
	if before != 12 {
		t.Fatalf("setup inserted %d spans, want 12", before)
	}

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var after int
	if err := repo.DB().QueryRow("SELECT COUNT(*) FROM spans").Scan(&after); err != nil {
		t.Fatalf("count: %v", err)
	}
	// Down to the 15-minute floor, everything inserted here is older than the
	// window, so the loop should have evicted all of it.
	if after != 0 {
		t.Errorf("remaining spans = %d, want 0 — the reclaim loop should keep "+
			"halving the window until the volume fits or it hits the floor", after)
	}
}

// TestReclaimStopsWhenAlreadyUnderTarget guards the other direction: a critical
// reading that is nonetheless under target must not evict anything. Without
// this, a mis-set watermark would quietly delete production telemetry.
func TestReclaimStopsWhenAlreadyUnderTarget(t *testing.T) {
	cfg := Config{
		InterestingRetentionHours: 48,
		ErrorRetentionDays:        30,
		ErrorLogRetentionDays:     30,
		AggregateRetentionDays:    365,
		SlowThresholdUS:           1_000_000,
		Watermarks:                Watermarks{Elevated: 0.0001, Critical: 0.0002},
		TargetFraction:            0.999, // any real volume is under this
	}
	worker, repo := setupDiskWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// 20h old: inside the critical-tier 12h window? No — outside it, so the
	// tiering deletes it. Use 2h so only an emergency eviction could remove it.
	insertTestSpans(t, repo, 4, "ok", 500, time.Now().UTC().Add(-2*time.Hour))

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var after int
	if err := repo.DB().QueryRow("SELECT COUNT(*) FROM spans").Scan(&after); err != nil {
		t.Fatalf("count: %v", err)
	}
	if after != 4 {
		t.Errorf("remaining spans = %d, want 4 — nothing should be evicted when "+
			"already under target", after)
	}
}

// TestReclaimReleasesAndRestoresBallast pins the self-heal mechanism: the
// reserve must be handed back before eviction (so a 100%-full volume has room
// to commit its DELETEs) and restored afterwards (so the next emergency has a
// way out too).
func TestReclaimReleasesAndRestoresBallast(t *testing.T) {
	cfg := Config{
		InterestingRetentionHours: 48,
		ErrorRetentionDays:        30,
		ErrorLogRetentionDays:     30,
		AggregateRetentionDays:    365,
		SlowThresholdUS:           1_000_000,
		Watermarks:                Watermarks{Elevated: 0.0001, Critical: 0.0002},
		TargetFraction:            0.999, // under target => loop no-ops, ballast restored
		BallastBytes:              1 << 20,
	}
	worker, _ := setupDiskWorker(t, cfg)

	ballast := worker.ballastFor(worker.cfg)
	if err := ballast.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !ballast.Present() {
		t.Fatal("ballast not present before reclaim")
	}

	space, err := worker.repo.(spaceReporter).DBSpace(context.Background(), worker.cfg.DBPath)
	if err != nil {
		t.Fatalf("DBSpace: %v", err)
	}
	if err := worker.reclaimToTarget(context.Background(), worker.cfg, space); err != nil {
		t.Fatalf("reclaimToTarget: %v", err)
	}

	// Back under target, so the reserve must have been re-taken.
	if !ballast.Present() {
		t.Error("ballast was not restored after reclaiming to target")
	}
	if _, err := os.Stat(ballast.Path()); err != nil {
		t.Errorf("stat ballast: %v", err)
	}
}

// TestPressuredTickIsShorter pins that a critical volume is re-checked in
// seconds rather than minutes. Five minutes is fine when there is room; when
// the disk is filling it is most of the margin.
func TestPressuredTickIsShorter(t *testing.T) {
	w := &RetentionWorker{cfg: Config{Interval: 5 * time.Minute}}

	if got := w.nextInterval(); got != 5*time.Minute {
		t.Errorf("unpressured interval = %v, want 5m", got)
	}
	w.pressured.Store(true)
	if got := w.nextInterval(); got != pressuredInterval {
		t.Errorf("pressured interval = %v, want %v", got, pressuredInterval)
	}

	// A configured interval already shorter than the pressured one must win —
	// pressure should never slow retention down.
	fast := &RetentionWorker{cfg: Config{Interval: 5 * time.Second}}
	fast.pressured.Store(true)
	if got := fast.nextInterval(); got != 5*time.Second {
		t.Errorf("interval = %v, want the configured 5s", got)
	}
}
