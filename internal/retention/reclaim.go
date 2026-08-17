package retention

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const (
	// reclaimRounds bounds the emergency loop. Each round halves the window, so
	// 10 rounds takes a 48h window below 3 minutes — far past any sane floor.
	// A bound matters because the loop is driven by a measurement that a
	// concurrent writer keeps moving.
	reclaimRounds = 10
	// reclaimFloorMinutes is the shortest window the emergency loop will impose.
	// Below this we are deleting telemetry as fast as it arrives, which is a
	// capacity problem no amount of deleting will fix — better to stop, stay
	// loud, and let admission control shed instead.
	reclaimFloorMinutes = 15
)

// needsReclaim reports whether usage is past the point where shortened windows
// alone are enough and we must actively evict down to a target.
func needsReclaim(used float64, w Watermarks) bool {
	return TierFor(used, w) == TierCritical
}

// ballast returns the worker's reserved-space handle, creating it on first use.
func (w *RetentionWorker) ballastFor(cfg Config) *repository.Ballast {
	w.ballastOnce.Do(func() {
		w.ballast = repository.NewBallast(cfg.DBPath, cfg.BallastBytes)
	})
	return w.ballast
}

// reclaimToTarget drives the volume back under cfg.TargetFraction by evicting
// the oldest raw telemetry, halving the window each round until it fits.
//
// This is the part that actually bounds the database. The tiering in pressure.go
// shortens windows by a fixed factor, which is a guess: if ingest volume rises
// enough, even a quartered window does not fit, and we sail into SQLITE_FULL
// anyway. This loop instead measures, evicts, and measures again — so the cap
// is a size, not a duration.
//
// It releases the ballast first. On a volume at 100% every DELETE fails, since
// committing one is itself a write; handing back the reserve is a metadata
// operation that succeeds regardless and buys exactly the room needed to dig
// out. The ballast is restored once we are back under target.
func (w *RetentionWorker) reclaimToTarget(ctx context.Context, cfg Config, space repository.Space) error {
	reporter, ok := w.repo.(spaceReporter)
	if !ok {
		return nil
	}

	ballast := w.ballastFor(cfg)
	w.releaseBallast(ballast)

	target := reclaimTarget(cfg)
	res, err := w.evictUntilUnderTarget(ctx, cfg, reporter, space.UsedFraction(), target)
	if err != nil {
		return err
	}
	w.reportReclaim(res, target)

	// Restore the reserve so the next emergency has a way out too — but only
	// once we are actually back under target. Re-taking it while still full
	// would just re-create the condition we escaped.
	if res.used <= target {
		if err := ballast.Ensure(); err != nil {
			w.logger.Warn("retention: could not restore ballast", "error", err)
		}
	}
	return nil
}

// reclaimResult is the outcome of an emergency eviction pass.
type reclaimResult struct {
	used          float64
	rounds        int
	evicted       int64
	windowMinutes int
}

// reclaimTarget is the volume-used level to evict back down to.
func reclaimTarget(cfg Config) float64 {
	if cfg.TargetFraction <= 0 || cfg.TargetFraction >= 1 {
		return 0.70
	}
	return cfg.TargetFraction
}

// startWindowMinutes is the window the emergency loop starts halving from.
func startWindowMinutes(cfg Config) int {
	if cfg.InterestingRetentionHours <= 0 {
		return 48 * 60
	}
	return cfg.InterestingRetentionHours * 60
}

// releaseBallast hands the reserve back to the filesystem so the DELETEs below
// have room to commit even on a volume at 100%.
func (w *RetentionWorker) releaseBallast(ballast *repository.Ballast) {
	freed, err := ballast.Release()
	if err != nil {
		w.logger.Warn("retention: could not release ballast", "error", err)
		return
	}
	if freed > 0 {
		w.logger.Warn("retention: released ballast to make room for eviction",
			"freed_bytes", freed, "path", ballast.Path())
	}
}

// evictUntilUnderTarget halves the retention window and evicts, re-measuring
// each round, until the volume is under target or the floor is reached.
func (w *RetentionWorker) evictUntilUnderTarget(
	ctx context.Context, cfg Config, reporter spaceReporter, used, target float64,
) (reclaimResult, error) {
	res := reclaimResult{used: used, windowMinutes: startWindowMinutes(cfg)}

	for res.rounds = 0; res.rounds < reclaimRounds && res.used > target; res.rounds++ {
		res.windowMinutes = max(res.windowMinutes/2, reclaimFloorMinutes)
		cutoff := time.Now().UTC().Add(-time.Duration(res.windowMinutes) * time.Minute)

		n, err := w.evictOlderThan(ctx, cfg, cutoff)
		if err != nil {
			return res, err
		}
		res.evicted += n

		space, err := reporter.DBSpace(ctx, cfg.DBPath)
		if err != nil {
			return res, err
		}
		res.used = space.UsedFraction()
		w.logger.Warn("retention: emergency eviction round",
			"round", res.rounds+1,
			"window_minutes", res.windowMinutes,
			"rows_evicted", n,
			"volume_used_pct", int(res.used*100),
			"target_pct", int(target*100))

		if res.windowMinutes <= reclaimFloorMinutes {
			break
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
	}
	return res, nil
}

// reportReclaim states the outcome. Reaching the floor while still over target
// means ingest is outrunning eviction — a capacity problem, not a retention
// one — so it is an error rather than a warning.
func (w *RetentionWorker) reportReclaim(res reclaimResult, target float64) {
	fields := []any{
		"volume_used_pct", int(res.used * 100),
		"target_pct", int(target * 100),
		"rounds", res.rounds,
		"rows_evicted", res.evicted,
		"window_minutes", res.windowMinutes,
	}
	if res.used > target {
		w.logger.Error("retention: could not reclaim to target — ingest is outrunning eviction", fields...)
		return
	}
	w.logger.Warn("retention: reclaimed to target", fields...)
}

// evictOlderThan deletes raw telemetry older than cutoff across every table
// that grows with traffic. Errors, aggregates and error logs are left alone for
// the same reason the tiering leaves them alone: they are the evidence an
// operator needs about the incident that caused the pressure.
func (w *RetentionWorker) evictOlderThan(ctx context.Context, cfg Config, cutoff time.Time) (int64, error) {
	var total int64

	n, err := w.repo.DeleteSpansOlderThan(cutoff)
	if err != nil {
		return total, err
	}
	total += n

	if n, err := w.repo.DeleteMetricsOlderThan(ctx, cutoff); err != nil {
		return total, err
	} else {
		total += n
	}

	errorLogCutoff := time.Now().UTC().Add(-time.Duration(cfg.ErrorLogRetentionDays) * 24 * time.Hour)
	if n, err := w.repo.DeleteLogsOlderThan(ctx, cutoff, errorLogCutoff); err != nil {
		return total, err
	} else {
		total += n
	}

	errorCutoff := time.Now().UTC().Add(-time.Duration(cfg.ErrorRetentionDays) * 24 * time.Hour)
	if n, err := w.repo.DeleteTraceSummariesOlderThan(ctx, cutoff, errorCutoff); err != nil {
		return total, err
	} else {
		total += n
	}

	return total, nil
}
