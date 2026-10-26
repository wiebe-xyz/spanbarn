package retention

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/rollup"
)

// spanRollupTierWindows lists the span_rollups tiers finest first, with how
// long each is kept.
func spanRollupTierWindows(cfg Config) []struct {
	step   int64
	window time.Duration
	label  string
} {
	return []struct {
		step   int64
		window time.Duration
		label  string
	}{
		{rollup.Step5m, time.Duration(cfg.SpanRollup5mHours) * time.Hour, "span 5m"},
		{rollup.StepHour, time.Duration(cfg.SpanRollupHourlyDays) * 24 * time.Hour, "span hourly"},
		{rollup.StepDay, time.Duration(cfg.SpanRollupDailyDays) * 24 * time.Hour, "span daily"},
	}
}

// deleteAllRollupTiers trims the metric rollup tiers and then the span rollup
// tiers, each under its own budget.
func (w *RetentionWorker) deleteAllRollupTiers(ctx context.Context, cfg Config, now time.Time, budget int64, st *cycleStats) error {
	var err error
	st.rollupRowsDeleted, st.rollupBacklogRemains, err = w.deleteRollupTiers(ctx, cfg, now, budget)
	if err != nil {
		return err
	}
	var more bool
	st.spanRollupRowsDeleted, more, err = w.deleteSpanRollupTiers(ctx, cfg, now, budget)
	st.rollupBacklogRemains = st.rollupBacklogRemains || more
	return err
}

// deleteSpanRollupTiers drops each span rollup tier past its window, but
// never past the point the compactor has rolled it into the tier above, and
// never more than budget rows in total. The daily tier is the top of the
// ladder, so its window alone decides. It reports whether it left a backlog.
func (w *RetentionWorker) deleteSpanRollupTiers(ctx context.Context, cfg Config, now time.Time, budget int64) (int64, bool, error) {
	var total int64
	for _, tier := range spanRollupTierWindows(cfg) {
		if total >= budget {
			return total, true, nil
		}
		cutoff := now.Add(-tier.window)
		if tier.step != rollup.StepDay {
			through, ok, err := w.repo.SpanRollupWatermark(ctx, tier.step)
			if err != nil {
				return total, true, err
			}
			if !ok {
				continue // not compacted yet: nothing of it may go
			}
			if through.Before(cutoff) {
				cutoff = through
			}
		}
		n, more, err := w.repo.DeleteSpanRollupsOlderThanLimited(ctx, tier.step, cutoff, budget-total)
		total += n
		if err != nil {
			return total, true, err
		}
		if n > 0 {
			w.logger.Info("retention: span rollup tier trimmed",
				"tier", tier.label, "rows_deleted", n, "cutoff", cutoff.UTC(), "backlog_remains", more)
		}
		if more {
			return total, true, nil
		}
	}
	return total, false, nil
}
