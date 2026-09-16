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

const (
	// maxRollupRowsPerCycle bounds how many rollup rows one retention cycle
	// deletes. A first run after compaction starts has a backlog of millions,
	// and draining it in one call holds the cycle open for as long as that
	// takes: production went twelve minutes without a single retention log line
	// or delete of anything else while one such call ground away underneath.
	// Capped, the backlog drains across cycles and every cycle still measures,
	// reports, and lets the rest of retention run.
	maxRollupRowsPerCycle int64 = 200_000
	// maxRollupRowsPerRound is the much smaller ceiling for one emergency
	// eviction round. The reclaim loop runs up to ten rounds per cycle and
	// re-measures the volume between them, so a round has to be short enough
	// that the measurement still means something.
	maxRollupRowsPerRound int64 = 25_000
)

// deleteRollupTiers drops each rollup tier past its own window, but never past
// the point the compactor has rolled that tier into the one above it, and never
// more than budget rows in total. It reports whether it left a backlog.
//
// The watermark gate is what makes short windows safe here. The 5-minute tier is
// kept for days because everything older has been summarised into hours, days,
// weeks and months. If compaction stalls, the watermark stops advancing and
// deletion stops with it, so the data waits instead of disappearing, at every
// pressure level.
func (w *RetentionWorker) deleteRollupTiers(ctx context.Context, cfg Config, now time.Time, budget int64) (int64, bool, error) {
	var total int64
	var backlog bool

	for _, tier := range rollupTierWindows(cfg) {
		if err := ctx.Err(); err != nil {
			return total, true, err
		}
		if total >= budget {
			return total, true, nil // out of budget; the rest waits for the next cycle
		}
		if tier.days <= 0 {
			continue // 0 means keep indefinitely, which is the monthly default
		}

		cutoff, ok, err := w.rollupCutoff(ctx, tier.step, now.Add(-time.Duration(tier.days)*24*time.Hour))
		if err != nil {
			return total, backlog, err
		}
		if !ok {
			continue
		}

		n, more, err := w.deleteRollupTier(ctx, tier.step, cutoff, budget-total)
		total += n
		if err != nil {
			return total, true, err
		}
		if n > 0 {
			w.logger.Info("retention: rollup tier trimmed",
				"tier", tier.label, "rows_deleted", n,
				"cutoff", cutoff.UTC(), "backlog_remains", more)
		}
		if more {
			backlog = true
		}
	}
	return total, backlog, nil
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

// deleteRollupTier removes up to budget rows of one tier older than cutoff,
// reporting whether more remain. The 5-minute tier is the accumulator's own
// table; every coarser tier shares metric_rollups_coarse.
func (w *RetentionWorker) deleteRollupTier(ctx context.Context, step int64, cutoff time.Time, budget int64) (int64, bool, error) {
	if budget <= 0 {
		return 0, true, nil
	}
	if step == rollup.Step5m {
		return w.repo.DeleteMetricRollupsOlderThanLimited(ctx, cutoff, budget)
	}
	return w.repo.DeleteCoarseRollupsOlderThanLimited(ctx, step, cutoff, budget)
}
