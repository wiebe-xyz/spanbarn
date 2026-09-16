package retention

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/rollup"
)

// rollupTierWindow pairs a rollup tier with how long it is kept.
type rollupTierWindow struct {
	step  int64
	days  int
	label string
}

// rollupTierWindows lists the ladder newest-resolution first.
func rollupTierWindows(cfg Config) []rollupTierWindow {
	return []rollupTierWindow{
		{step: rollup.Step5m, days: cfg.MetricRollupRetentionDays, label: "5m"},
		{step: rollup.StepHour, days: cfg.HourlyRollupDays, label: "hourly"},
		{step: rollup.StepDay, days: cfg.DailyRollupDays, label: "daily"},
		{step: rollup.StepWeek, days: cfg.WeeklyRollupDays, label: "weekly"},
		{step: rollup.StepMonth, days: cfg.MonthlyRollupDays, label: "monthly"},
	}
}

// deleteRollupTiers drops each rollup tier past its own window, but never past
// the point the compactor has rolled that tier into the one above it.
//
// That gate is what makes short windows safe here. The 5-minute tier is kept for
// days, not because two days of it is all anyone wants, but because everything
// older has been summarised into hours, days, weeks and months. If compaction
// stalls, the watermark stops advancing and deletion stops with it — the data
// waits instead of disappearing, at every pressure level.
func (w *RetentionWorker) deleteRollupTiers(ctx context.Context, cfg Config, now time.Time) (int64, error) {
	var total int64
	for _, tier := range rollupTierWindows(cfg) {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if tier.days <= 0 {
			continue // 0 means keep indefinitely, which is the monthly default
		}

		cutoff, ok, err := w.rollupCutoff(ctx, tier.step, now.Add(-time.Duration(tier.days)*24*time.Hour))
		if err != nil {
			return total, err
		}
		if !ok {
			continue
		}

		n, err := w.deleteRollupTier(ctx, tier.step, cutoff)
		if err != nil {
			return total, err
		}
		if n > 0 {
			w.logger.Info("retention: rollup tier trimmed",
				"tier", tier.label, "rows_deleted", n, "cutoff", cutoff.UTC())
		}
		total += n
	}
	return total, nil
}

// rollupCutoff limits a tier's window cutoff to its compaction watermark.
// A tier with no watermark yet has not been compacted at all, so nothing of it
// may be deleted. The coarsest tier is the exception: nothing compacts it
// further, so its own window is the whole story.
func (w *RetentionWorker) rollupCutoff(ctx context.Context, step int64, windowCutoff time.Time) (time.Time, bool, error) {
	if step == rollup.StepMonth {
		return windowCutoff, true, nil
	}
	through, ok, err := w.repo.MetricRollupWatermark(ctx, step)
	if err != nil {
		return time.Time{}, false, err
	}
	if !ok {
		return time.Time{}, false, nil
	}
	if through.Before(windowCutoff) {
		return through, true, nil
	}
	return windowCutoff, true, nil
}

// deleteRollupTier removes one tier's rows older than cutoff. The 5-minute tier
// is the accumulator's own table; every coarser tier shares metric_rollups_coarse.
func (w *RetentionWorker) deleteRollupTier(ctx context.Context, step int64, cutoff time.Time) (int64, error) {
	if step == rollup.Step5m {
		return w.repo.DeleteMetricRollupsOlderThan(ctx, cutoff)
	}
	return w.repo.DeleteCoarseRollupsOlderThan(ctx, step, cutoff)
}
