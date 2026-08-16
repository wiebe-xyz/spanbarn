package retention

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestTierFor(t *testing.T) {
	w := Watermarks{Elevated: 0.75, Critical: 0.90}
	tests := []struct {
		used float64
		want Tier
	}{
		{0, TierNormal},
		{0.5, TierNormal},
		{0.749, TierNormal},
		{0.75, TierElevated},
		{0.89, TierElevated},
		{0.90, TierCritical},
		{1.0, TierCritical},
	}
	for _, tt := range tests {
		if got := TierFor(tt.used, w); got != tt.want {
			t.Errorf("TierFor(%v) = %v, want %v", tt.used, got, tt.want)
		}
	}
}

// TestWatermarksDefaults covers the config-validation shape this codebase keeps
// getting burned by: a zero or nonsensical value must fall back to a sane
// default rather than silently disabling the guard (0 would make everything
// critical; 1 would make nothing ever trigger).
func TestWatermarksDefaults(t *testing.T) {
	got := Watermarks{}.withDefaults()
	if got.Elevated != 0.75 || got.Critical != 0.90 {
		t.Errorf("zero Watermarks = %+v, want defaults 0.75/0.90", got)
	}

	if got := (Watermarks{Elevated: 1.5, Critical: -1}).withDefaults(); got.Elevated != 0.75 || got.Critical != 0.90 {
		t.Errorf("out-of-range Watermarks = %+v, want defaults", got)
	}

	// Inverted watermarks must not leave critical below elevated, or a database
	// would be "critical" while still merely "elevated".
	if got := (Watermarks{Elevated: 0.9, Critical: 0.5}).withDefaults(); got.Critical < got.Elevated {
		t.Errorf("inverted Watermarks = %+v, want critical >= elevated", got)
	}
}

func TestTierApplyShortensRawTelemetryOnly(t *testing.T) {
	cfg := Config{
		InterestingRetentionHours: 48,
		BoringRetentionMinutes:    30,
		MetricsRetentionDays:      8,
		LogRetentionHours:         24,
		// Derived data — must survive untouched at every tier.
		ErrorRetentionDays:     30,
		AggregateRetentionDays: 365,
		ErrorLogRetentionDays:  30,
	}

	if got := TierNormal.Apply(cfg); got != cfg {
		t.Errorf("TierNormal.Apply changed the config: %+v", got)
	}

	elevated := TierElevated.Apply(cfg)
	if elevated.InterestingRetentionHours != 24 {
		t.Errorf("elevated InterestingRetentionHours = %d, want 24", elevated.InterestingRetentionHours)
	}
	if elevated.MetricsRetentionDays != 4 {
		t.Errorf("elevated MetricsRetentionDays = %d, want 4", elevated.MetricsRetentionDays)
	}
	if elevated.LogRetentionHours != 12 {
		t.Errorf("elevated LogRetentionHours = %d, want 12", elevated.LogRetentionHours)
	}
	if elevated.BoringRetentionMinutes != 15 {
		t.Errorf("elevated BoringRetentionMinutes = %d, want 15", elevated.BoringRetentionMinutes)
	}

	critical := TierCritical.Apply(cfg)
	if critical.InterestingRetentionHours != 12 {
		t.Errorf("critical InterestingRetentionHours = %d, want 12", critical.InterestingRetentionHours)
	}
	if critical.MetricsRetentionDays != 2 {
		t.Errorf("critical MetricsRetentionDays = %d, want 2", critical.MetricsRetentionDays)
	}

	// The whole point of leaving these alone: an operator investigating the
	// incident that filled the disk must still find the error evidence.
	for _, tier := range []Tier{TierElevated, TierCritical} {
		got := tier.Apply(cfg)
		if got.ErrorRetentionDays != cfg.ErrorRetentionDays {
			t.Errorf("%v shortened ErrorRetentionDays to %d", tier, got.ErrorRetentionDays)
		}
		if got.AggregateRetentionDays != cfg.AggregateRetentionDays {
			t.Errorf("%v shortened AggregateRetentionDays to %d", tier, got.AggregateRetentionDays)
		}
		if got.ErrorLogRetentionDays != cfg.ErrorLogRetentionDays {
			t.Errorf("%v shortened ErrorLogRetentionDays to %d", tier, got.ErrorLogRetentionDays)
		}
	}
}

// TestTierApplyNeverCollapsesToZero pins the floors. A window scaled to 0 would
// delete telemetry the instant it arrived — a worse outage than the disk-full
// it is avoiding.
func TestTierApplyNeverCollapsesToZero(t *testing.T) {
	tiny := Config{
		InterestingRetentionHours: 1,
		BoringRetentionMinutes:    1,
		MetricsRetentionDays:      1,
		LogRetentionHours:         1,
	}
	got := TierCritical.Apply(tiny)
	if got.InterestingRetentionHours < 1 {
		t.Errorf("InterestingRetentionHours = %d, want >= 1", got.InterestingRetentionHours)
	}
	if got.BoringRetentionMinutes < 5 {
		t.Errorf("BoringRetentionMinutes = %d, want >= 5", got.BoringRetentionMinutes)
	}
	if got.MetricsRetentionDays < 1 {
		t.Errorf("MetricsRetentionDays = %d, want >= 1", got.MetricsRetentionDays)
	}
	if got.LogRetentionHours < 1 {
		t.Errorf("LogRetentionHours = %d, want >= 1", got.LogRetentionHours)
	}
}

// setupDiskWorker builds a worker over a REAL on-disk database, which is what
// the pressure probe needs (an in-memory DB has no volume to measure).
func setupDiskWorker(t *testing.T, cfg Config) (*RetentionWorker, *repository.Repository) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spanbarn.db")

	db, err := repository.NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	repo := repository.NewRepository(db.DB)
	logger := slog.Default()
	cfg.BatchYield = time.Millisecond
	cfg.DBPath = path
	return NewRetentionWorker(repo, aggregation.NewAggregator(repo, time.Minute, logger), cfg, logger), repo
}

func TestApplyDiskPressure(t *testing.T) {
	base := Config{
		InterestingRetentionHours: 48,
		BoringRetentionMinutes:    30,
		MetricsRetentionDays:      8,
		LogRetentionHours:         24,
		ErrorRetentionDays:        30,
		AggregateRetentionDays:    365,
	}

	t.Run("no pressure leaves the config alone", func(t *testing.T) {
		// A watermark just under 1 cannot be reached by a working test volume.
		cfg := base
		cfg.Watermarks = Watermarks{Elevated: 0.999, Critical: 0.9999}
		worker, _ := setupDiskWorker(t, cfg)

		got := worker.applyDiskPressure(context.Background(), worker.cfg)
		if got.InterestingRetentionHours != 48 {
			t.Errorf("InterestingRetentionHours = %d, want 48 (unpressured)", got.InterestingRetentionHours)
		}
	})

	t.Run("critical pressure shortens the windows", func(t *testing.T) {
		// Any real volume is more than 0.02% used, so this forces critical.
		cfg := base
		cfg.Watermarks = Watermarks{Elevated: 0.0001, Critical: 0.0002}
		worker, _ := setupDiskWorker(t, cfg)

		got := worker.applyDiskPressure(context.Background(), worker.cfg)
		if got.InterestingRetentionHours != 12 {
			t.Errorf("InterestingRetentionHours = %d, want 12 (critical)", got.InterestingRetentionHours)
		}
		if got.MetricsRetentionDays != 2 {
			t.Errorf("MetricsRetentionDays = %d, want 2 (critical)", got.MetricsRetentionDays)
		}
		if got.ErrorRetentionDays != 30 {
			t.Errorf("ErrorRetentionDays = %d, want 30 (never shortened)", got.ErrorRetentionDays)
		}
	})

	t.Run("empty DBPath disables the probe", func(t *testing.T) {
		cfg := base
		cfg.Watermarks = Watermarks{Elevated: 0.0001, Critical: 0.0002}
		worker, _ := setupDiskWorker(t, cfg)
		worker.cfg.DBPath = ""

		got := worker.applyDiskPressure(context.Background(), worker.cfg)
		if got.InterestingRetentionHours != 48 {
			t.Errorf("InterestingRetentionHours = %d, want 48 (probe disabled)", got.InterestingRetentionHours)
		}
	})
}

// TestRunOnceDropsSpansEarlierUnderDiskPressure is the regression test for the
// production outage: with a 48h configured window and a filling volume, spans
// older than the SHORTENED window must be deleted on this cycle rather than
// surviving until the disk is full. Without the guard this test fails — a
// 20-hour-old span sits well inside the configured 48h window.
func TestRunOnceDropsSpansEarlierUnderDiskPressure(t *testing.T) {
	cfg := Config{
		InterestingRetentionHours: 48,
		ErrorRetentionDays:        30,
		AggregateRetentionDays:    365,
		SlowThresholdUS:           1_000_000,
		// Force critical: any real volume exceeds 0.02% used.
		Watermarks: Watermarks{Elevated: 0.0001, Critical: 0.0002},
	}
	worker, repo := setupDiskWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// 20h old: inside the configured 48h window, outside the critical 12h one.
	insertTestSpans(t, repo, 5, "ok", 500, time.Now().UTC().Add(-20*time.Hour))
	// 2h old: inside both windows, must survive.
	insertTestSpans(t, repo, 3, "ok", 500, time.Now().UTC().Add(-2*time.Hour))

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var remaining int
	if err := repo.DB().QueryRow("SELECT COUNT(*) FROM spans").Scan(&remaining); err != nil {
		t.Fatalf("count spans: %v", err)
	}
	if remaining != 3 {
		t.Errorf("remaining spans = %d, want 3 — disk pressure should have dropped "+
			"the 20h-old spans despite the configured 48h window", remaining)
	}
}

// TestRunOnceKeepsSpansWithoutDiskPressure is the other half: the guard must
// not shorten anything on a healthy volume, or it would silently destroy data.
func TestRunOnceKeepsSpansWithoutDiskPressure(t *testing.T) {
	cfg := Config{
		InterestingRetentionHours: 48,
		ErrorRetentionDays:        30,
		AggregateRetentionDays:    365,
		SlowThresholdUS:           1_000_000,
		Watermarks:                Watermarks{Elevated: 0.999, Critical: 0.9999},
	}
	worker, repo := setupDiskWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	insertTestSpans(t, repo, 5, "ok", 500, time.Now().UTC().Add(-20*time.Hour))

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var remaining int
	if err := repo.DB().QueryRow("SELECT COUNT(*) FROM spans").Scan(&remaining); err != nil {
		t.Fatalf("count spans: %v", err)
	}
	if remaining != 5 {
		t.Errorf("remaining spans = %d, want 5 — an unpressured volume must keep "+
			"the full configured window", remaining)
	}
}
