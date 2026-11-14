package service

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

// FineTailWidth bounds the 5-minute read that an hour tier read takes its newest
// buckets from. The compactor closes an hour about 30 minutes after it ends, so
// the tail is normally under two hours wide. A quarter of the width a filter
// already reads at the 5-minute tier leaves room for a slow compactor, and a
// stalled one leaves a gap the stall alert reports.
const FineTailWidth = 6 * time.Hour

// hourTierWithTail reads the hour tier up to its compaction watermark and the
// 5-minute buckets after it, oldest first. The hours the compactor has not
// reached yet are only in the 5-minute tier, so an hour tier read alone left
// out the newest hour and showed nothing for a series younger than that (#497).
// The tail starts at the watermark, which keeps a bucket from being read from
// both tiers, and covers at most FineTailWidth. The tail is read first: the
// rows limit keeps the newest buckets.
func (s *MetricsService) hourTierWithTail(ctx context.Context, f MetricRollupFilter, from time.Time) ([]MetricRollup, error) {
	through, ok, err := s.repo.MetricRollupWatermark(ctx, tier.Step5m)
	if err != nil {
		return nil, err
	}
	tailFrom := tailStart(f.To, through, ok, from)
	hf := f
	var tail []MetricRollup
	if tailFrom.Before(f.To) {
		tf := f
		tf.From = tailFrom
		if tail, err = s.Rollups(ctx, tf); err != nil {
			return nil, err
		}
		for i := range tail {
			tail[i].StepSeconds = tier.Step5m
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
	hours, err := s.queryTier(ctx, tier.StepHour, hf)
	if err != nil {
		return nil, err
	}
	for i := range hours {
		hours[i].StepSeconds = tier.StepHour
	}
	return append(hours, tail...), nil
}

// tailStart is where the 5-minute tail of an hour tier read up to to begins:
// at the watermark through, no earlier than FineTailWidth before to, rounded up
// to an hour so the hour buckets before it end where it starts, and no earlier
// than the bucket holding from.
func tailStart(to, through time.Time, ok bool, from time.Time) time.Time {
	start := to.Add(-FineTailWidth)
	if hour := tier.TruncateBucket(start, tier.StepHour); hour.Before(start) {
		start = tier.NextBucket(hour, tier.StepHour)
	}
	if ok && through.After(start) {
		start = through
	}
	if first := tier.TruncateBucket(from, tier.Step5m); first.After(start) {
		start = first
	}
	return start
}
