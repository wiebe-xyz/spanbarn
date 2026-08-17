package retention

import (
	"context"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Tier is how much disk pressure the database is under. Retention windows are
// shortened as the tier rises, so a volume that is filling drops raw telemetry
// earlier instead of waiting for a fixed clock that no longer fits.
type Tier int

const (
	// TierNormal — plenty of room; configured windows apply unchanged.
	TierNormal Tier = iota
	// TierElevated — the volume is filling faster than retention is freeing it.
	// Halve the raw-telemetry windows to bend the curve before it is urgent.
	TierElevated
	// TierCritical — the volume is nearly full. Cut hard: at this point the
	// alternative is SQLITE_FULL, which takes down logins and every write at
	// once and cannot be recovered from in place (a full volume has no room to
	// commit the DELETEs that would free it).
	TierCritical
)

func (t Tier) String() string {
	switch t {
	case TierElevated:
		return "elevated"
	case TierCritical:
		return "critical"
	default:
		return "normal"
	}
}

// factor is how much of each configured raw-telemetry window survives at this tier.
func (t Tier) factor() float64 {
	switch t {
	case TierElevated:
		return 0.5
	case TierCritical:
		return 0.25
	default:
		return 1
	}
}

// Watermarks are the volume-used fractions at which each tier engages.
type Watermarks struct {
	Elevated float64 // default 0.75
	Critical float64 // default 0.90
}

func (w Watermarks) withDefaults() Watermarks {
	if w.Elevated <= 0 || w.Elevated >= 1 {
		w.Elevated = 0.75
	}
	if w.Critical <= 0 || w.Critical >= 1 {
		w.Critical = 0.90
	}
	if w.Critical < w.Elevated {
		w.Critical = w.Elevated
	}
	return w
}

// TierFor maps a volume-used fraction onto a pressure tier.
func TierFor(usedFraction float64, w Watermarks) Tier {
	w = w.withDefaults()
	switch {
	case usedFraction >= w.Critical:
		return TierCritical
	case usedFraction >= w.Elevated:
		return TierElevated
	default:
		return TierNormal
	}
}

// scale shrinks a window by the tier's factor, never below floor. The floor
// matters: a window that collapses to zero would delete data the instant it
// arrives, which is worse than the disk-full it is trying to avoid.
func scale(v int, factor float64, floor int) int {
	if factor >= 1 {
		return v
	}
	scaled := int(float64(v) * factor)
	if scaled < floor {
		return floor
	}
	return scaled
}

// Apply shortens the raw-telemetry retention windows according to the tier.
//
// Only raw telemetry is shortened — spans, boring spans, metrics and logs. The
// derived data (aggregates, error samples, error logs) is deliberately left
// alone: it is what the product is actually for, it is far smaller per unit
// time, and dropping it would mean an operator investigating the incident that
// caused the disk pressure finds the evidence deleted.
func (t Tier) Apply(cfg Config) Config {
	f := t.factor()
	if f >= 1 {
		return cfg
	}
	cfg.InterestingRetentionHours = scale(cfg.InterestingRetentionHours, f, 1)
	cfg.BoringRetentionMinutes = scale(cfg.BoringRetentionMinutes, f, 5)
	cfg.MetricsRetentionDays = scale(cfg.MetricsRetentionDays, f, 1)
	cfg.LogRetentionHours = scale(cfg.LogRetentionHours, f, 1)
	return cfg
}

// spaceReporter is the subset of the repository that can measure disk usage.
// It is an optional capability rather than part of the Repository interface so
// that a worker built over a repository which cannot measure space (an
// in-memory test DB, say) simply runs unpressured instead of failing.
type spaceReporter interface {
	DBSpace(ctx context.Context, dbPath string) (repository.Space, error)
}

// applyDiskPressure samples the volume and shortens cfg's raw-telemetry windows
// if the disk is filling. Returns cfg unchanged when space cannot be measured,
// when pressure checking is disabled, or when the tier is normal.
func (w *RetentionWorker) applyDiskPressure(ctx context.Context, cfg Config) Config {
	if cfg.DBPath == "" {
		return cfg
	}
	reporter, ok := w.repo.(spaceReporter)
	if !ok {
		return cfg
	}
	space, err := reporter.DBSpace(ctx, cfg.DBPath)
	if err != nil {
		w.logger.Warn("retention: disk space probe failed", "error", err)
		return cfg
	}
	if !space.Measured() {
		return cfg
	}

	// auto_vacuum=NONE means deleted pages are never returned to the
	// filesystem, so retention can free rows without ever freeing bytes. That
	// is how a full volume becomes unrecoverable in place. It is a one-time
	// setup mistake and silent by nature, so say it out loud.
	if space.AutoVacuum == 0 {
		w.warnNoAutoVacuum.Do(func() {
			w.logger.Warn("database has auto_vacuum=NONE — deleted pages are never returned to the " +
				"filesystem, so retention frees rows but not disk. Rebuild with " +
				"'PRAGMA auto_vacuum=INCREMENTAL; VACUUM;' or the volume will fill and cannot self-heal.")
		})
	}

	used := space.UsedFraction()
	tier := TierFor(used, cfg.Watermarks)
	w.pressured.Store(tier == TierCritical)

	if tier == TierNormal {
		// Keep the reserve topped up while things are calm — that is the only
		// time it can be paid for. A ballast created lazily during an emergency
		// would be a write on a volume that has no room for writes.
		if b := w.ballastFor(cfg); b.Enabled() && !b.Present() {
			if err := b.Ensure(); err != nil {
				w.logger.Warn("retention: could not create disk ballast", "error", err)
			} else {
				w.logger.Info("retention: disk ballast reserved",
					"bytes", b.Size(), "path", b.Path())
			}
		}
		return cfg
	}

	// Past the critical watermark, shortened windows are not enough on their
	// own: they are a fixed multiplier applied to a duration, and the thing we
	// actually need to bound is a size. Evict until the volume fits.
	if needsReclaim(used, cfg.Watermarks) {
		if err := w.reclaimToTarget(ctx, cfg, space); err != nil {
			w.logger.Error("retention: emergency reclaim failed", "error", err)
		}
	}

	shortened := tier.Apply(cfg)
	w.logger.Warn("retention: disk pressure — shortening raw telemetry windows",
		"tier", tier.String(),
		"volume_used_pct", int(used*100),
		"volume_free_bytes", space.VolumeFreeBytes,
		"reusable_bytes", space.ReusableBytes(),
		"db_file_bytes", space.FileBytes,
		"interesting_hours", shortened.InterestingRetentionHours,
		"was_interesting_hours", cfg.InterestingRetentionHours,
		"metrics_days", shortened.MetricsRetentionDays,
		"was_metrics_days", cfg.MetricsRetentionDays,
		"log_hours", shortened.LogRetentionHours,
		"was_log_hours", cfg.LogRetentionHours,
	)
	return shortened
}
