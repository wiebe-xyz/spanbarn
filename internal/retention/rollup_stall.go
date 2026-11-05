package retention

import (
	"context"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/rollup"
)

// A compactor that has stopped leaves its tiers' watermarks where they are.
// Retention deletes each tier only up to the watermark of the tier compacted
// from it, so the rows below wait, and nothing else says so: a failed pass logs
// a warning, which does not reach BugBarn, and a compactor goroutine that has
// died logs nothing. Retention runs on its own goroutine, so it checks how far
// each tier lags behind its closed buckets and reports a stall at error level.

// stallReportInterval is how often a stall that goes on is reported again.
const stallReportInterval = time.Hour

// stallBuckets is how many closed target buckets a tier may owe before it
// counts as stalled. An hour tier drains a short pause within a few passes, so
// three hours behind is a stall. A day, week or month bucket closes only after
// the tier below has caught up past it, so two buckets behind is one more than
// a normal close leaves.
func stallBuckets(step int64) int {
	if step == rollup.StepHour {
		return 3
	}
	return 2
}

// stallTier is one rung of a compaction ladder: the target step it builds and
// the source step whose watermark records how far it got.
type stallTier struct {
	step, source int64
	label        string
}

// stallLadder is a compaction ladder and how to read its watermarks. off
// means its compactor was turned off on purpose, and setting names the switch.
type stallLadder struct {
	name      string
	tiers     []stallTier
	watermark func(ctx context.Context, step int64) (time.Time, bool, error)
	off       bool
	setting   string
}

func (w *RetentionWorker) stallLadders() []stallLadder {
	metric := stallLadder{name: "metric", watermark: w.repo.MetricRollupWatermark,
		off: w.cfg.MetricCompactionOff, setting: "SPANBARN_ROLLUP_COMPACT_ENABLED=0"}
	for _, t := range rollup.Ladder() {
		metric.tiers = append(metric.tiers, stallTier{t.Step, t.Source, t.Label})
	}
	span := stallLadder{name: "span", watermark: w.repo.SpanRollupWatermark,
		off: w.cfg.SpanCompactionOff, setting: "SPANBARN_SPAN_ROLLUPS=0", tiers: []stallTier{
			{rollup.StepHour, rollup.Step5m, "hourly"},
			{rollup.StepDay, rollup.StepHour, "daily"},
		}}
	return []stallLadder{metric, span}
}

// stallState remembers when each stalled tier was last reported, and which
// ladders' skip has been logged.
type stallState struct {
	mu       sync.Mutex
	reported map[string]time.Time
	skipped  map[string]bool
}

// bucketsBehind counts the closed target buckets of step after through, up to
// limit. A bucket is closed once MetricRollupLateWrite has passed its end.
func bucketsBehind(through time.Time, step int64, now time.Time, limit int) int {
	closedEnd := rollup.TruncateBucket(now.Add(-repository.MetricRollupLateWrite), step)
	n := 0
	for b := rollup.TruncateBucket(through, step); b.Before(closedEnd) && n < limit; b = rollup.NextBucket(b, step) {
		n++
	}
	return n
}

// reportRollupStalls logs every compaction tier that owes stallBuckets or more
// closed buckets at error level, once per stallReportInterval while it lasts,
// and logs the catch-up once it is back under. A tier with no watermark has not
// compacted yet and is left out, and so is a ladder whose compactor is off.
func (w *RetentionWorker) reportRollupStalls(ctx context.Context, now time.Time) {
	w.stalls.mu.Lock()
	defer w.stalls.mu.Unlock()
	if w.stalls.reported == nil {
		w.stalls.reported = map[string]time.Time{}
		w.stalls.skipped = map[string]bool{}
	}
	for _, ladder := range w.stallLadders() {
		// An operator who turns compaction off means the tiers to wait, so
		// that is no stall to report every hour.
		if ladder.off {
			if !w.stalls.skipped[ladder.name] {
				w.stalls.skipped[ladder.name] = true
				w.logger.Info("rollup stall check skipped: compaction is off", "ladder", ladder.name, "setting", ladder.setting)
			}
			continue
		}
		for _, tier := range ladder.tiers {
			through, ok, err := ladder.watermark(ctx, tier.source)
			if err != nil {
				w.logger.Warn("retention: read compaction watermark", "ladder", ladder.name, "tier", tier.label, "error", err)
				continue
			}
			if !ok {
				continue
			}
			key := ladder.name + "/" + tier.label
			limit := stallBuckets(tier.step)
			behind := bucketsBehind(through, tier.step, now, limit)
			last, wasStalled := w.stalls.reported[key]
			if behind < limit {
				if wasStalled {
					delete(w.stalls.reported, key)
					w.logger.Info("rollup compaction caught up", "ladder", ladder.name, "tier", tier.label, "watermark", through.UTC())
				}
				continue
			}
			if wasStalled && now.Sub(last) < stallReportInterval {
				continue
			}
			w.stalls.reported[key] = now
			w.logger.Error("rollup compaction stalled: retention holds the tier below",
				"ladder", ladder.name, "tier", tier.label,
				"buckets_behind_at_least", behind, "watermark", through.UTC())
		}
	}
}
