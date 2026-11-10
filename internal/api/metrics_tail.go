package api

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/rollup"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// fineTailWidth bounds the 5-minute read that an hour tier read takes its newest
// buckets from. The compactor closes an hour about 30 minutes after it ends, so
// the tail is normally under two hours wide. A quarter of the width a filter
// already reads at the 5-minute tier leaves room for a slow compactor, and a
// stalled one leaves a gap the stall alert reports.
const fineTailWidth = 6 * time.Hour

// hourTierWithTail reads the hour tier up to its compaction watermark and the
// 5-minute buckets after it, oldest first. The hours the compactor has not
// reached yet are only in the 5-minute tier, so an hour tier read alone left
// out the newest hour and showed nothing for a series younger than that (#497).
// The tail starts at the watermark, which keeps a bucket from being read from
// both tiers, and covers at most fineTailWidth. The tail is read first: the
// rows limit keeps the newest buckets.
func (h *metricsQueryHandlers) hourTierWithTail(ctx context.Context, f service.MetricRollupFilter, from time.Time) ([]service.MetricRollup, error) {
	through, ok, err := h.svc.HourTierThrough(ctx)
	if err != nil {
		return nil, err
	}
	tailFrom := tailStart(f.To, through, ok, from)
	hf := f
	var tail []service.MetricRollup
	if tailFrom.Before(f.To) {
		tf := f
		tf.From = tailFrom
		if tail, err = h.svc.Rollups(ctx, tf); err != nil {
			return nil, err
		}
		for i := range tail {
			tail[i].StepSeconds = rollup.Step5m
		}
		if f.Limit > 0 && len(tail) >= f.Limit {
			return tail, nil
		}
		if f.Limit > 0 {
			hf.Limit = f.Limit - len(tail)
		}
		// The filter's To bounds bucket starts, and an hour bucket belongs
		// before the tail only when it ends at or before tailFrom.
		hf.To = tailFrom.Add(-time.Hour)
		if hf.To.Before(hf.From) {
			return tail, nil
		}
	}
	hours, err := h.queryTier(ctx, rollup.StepHour, hf)
	if err != nil {
		return nil, err
	}
	for i := range hours {
		hours[i].StepSeconds = rollup.StepHour
	}
	return append(hours, tail...), nil
}

// tailStart is where the 5-minute tail of an hour tier read up to to begins:
// at the watermark through, no earlier than fineTailWidth before to, rounded up
// to an hour so the hour buckets before it end where it starts, and no earlier
// than the bucket holding from.
func tailStart(to, through time.Time, ok bool, from time.Time) time.Time {
	start := to.Add(-fineTailWidth)
	if hour := rollup.TruncateBucket(start, rollup.StepHour); hour.Before(start) {
		start = rollup.NextBucket(hour, rollup.StepHour)
	}
	if ok && through.After(start) {
		start = through
	}
	if first := rollup.TruncateBucket(from, rollup.Step5m); first.After(start) {
		start = first
	}
	return start
}
