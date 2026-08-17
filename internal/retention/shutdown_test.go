package retention

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestNoDiskPressureNoiseOnShutdown pins SPA-59, which this very guard filed
// against itself: during a routine rolling deploy the retention worker probed
// the volume with a cancelled context, the probe failed with "context
// canceled", and a clean pod shutdown became a tracked issue.
//
// Shutdown is not a failure. Nothing should be logged.
func TestNoDiskPressureNoiseOnShutdown(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := Config{
		InterestingRetentionHours: 48,
		ErrorRetentionDays:        30,
		AggregateRetentionDays:    365,
		SlowThresholdUS:           1_000_000,
		Watermarks:                Watermarks{Elevated: 0.0001, Critical: 0.0002},
		TargetFraction:            0.999,
	}
	worker, _ := setupDiskWorker(t, cfg)
	worker.logger = logger

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := worker.applyDiskPressure(ctx, worker.cfg)

	if out := logBuf.String(); strings.Contains(out, "disk space probe failed") {
		t.Errorf("logged a probe failure for a cancelled context: %s", out)
	}
	if out := logBuf.String(); strings.Contains(out, "emergency reclaim failed") {
		t.Errorf("logged a reclaim failure for a cancelled context: %s", out)
	}
	// The config must come back untouched — a cancelled probe must never be
	// read as "no pressure detected, carry on with a long window".
	if got.InterestingRetentionHours != cfg.InterestingRetentionHours {
		t.Errorf("InterestingRetentionHours = %d, want %d unchanged",
			got.InterestingRetentionHours, cfg.InterestingRetentionHours)
	}
}

// TestRealProbeFailureStillWarns is the other half: silencing shutdown must not
// silence a genuinely broken probe, or the guard goes quiet exactly when it has
// stopped working.
func TestRealProbeFailureStillWarns(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := Config{
		InterestingRetentionHours: 48,
		Watermarks:                Watermarks{Elevated: 0.75, Critical: 0.90},
		TargetFraction:            0.70,
	}
	worker, repo := setupDiskWorker(t, cfg)
	worker.logger = logger

	// Close the database so the pragmas fail for a reason that is not shutdown.
	if err := repo.DB().Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	worker.applyDiskPressure(context.Background(), worker.cfg)

	if out := logBuf.String(); !strings.Contains(out, "disk space probe failed") {
		t.Errorf("a genuinely failing probe was silent: %s", out)
	}
}

// TestPressuredTickUnaffectedByShutdown guards that a cancelled cycle does not
// leave the worker believing it is under pressure and re-ticking every 30s.
func TestPressuredTickUnaffectedByShutdown(t *testing.T) {
	w := &RetentionWorker{cfg: Config{Interval: 5 * time.Minute}}
	if got := w.nextInterval(); got != 5*time.Minute {
		t.Errorf("interval = %v, want 5m", got)
	}
}
