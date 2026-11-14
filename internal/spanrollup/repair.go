package spanrollup

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

// repairInterval is how often the compactor looks for coarse buckets that an
// earlier pass left out. The first pass after start looks right away.
const repairInterval = time.Hour

// tierTable is one table the compactor builds: span_rollups or span_edges.
type tierTable struct {
	count    BucketCounter
	arrivals SpanArrivals
	oldest   func(ctx context.Context, step int64) (time.Time, bool, error)
	build    func(ctx context.Context, rung Tier, start, end time.Time) (int, error)
}

func (c *Compactor) tables() []tierTable {
	return []tierTable{
		{count: c.repo, arrivals: c.repo, oldest: c.repo.OldestSpanRollupBucket, build: c.compactRollupBucket},
		{count: BucketCounterFunc(c.repo.CountSpanEdgeBuckets), arrivals: Arrivals(c.repo, c.repo),
			oldest: c.repo.OldestSpanEdgeBucket, build: c.compactEdgeBucket},
	}
}

// repairIfDue runs a repair pass over the ladder, finest first, so the daily
// tier sees the hours just repaired.
func (c *Compactor) repairIfDue(ctx context.Context) error {
	now := c.now()
	if !c.repaired.IsZero() && now.Sub(c.repaired) < repairInterval {
		return nil
	}
	c.repaired = now
	for _, rung := range Ladder() {
		buckets, rows, err := c.repairTier(ctx, rung)
		if err != nil {
			return err
		}
		if buckets > 0 {
			c.logger.Info("span rollup compaction repaired left-out buckets", "tier", rung.Label,
				"buckets", buckets, "rows_written", rows)
		}
	}
	return nil
}

// repairTier builds the coarse buckets of tier that a pass left out and that
// their source window can now build whole. A pass left a bucket out when its
// window held an empty source bucket; a binary from before quiet buckets
// counted as covered did that for the hour of every deploy. Only windows
// below the source watermark are repaired: the pass owns the ones above it.
func (c *Compactor) repairTier(ctx context.Context, rung Tier) (int, int, error) {
	through, ok, err := c.repo.SpanRollupWatermark(ctx, rung.Source)
	if err != nil || !ok {
		return 0, 0, err
	}
	var buckets, rows int
	for _, t := range c.tables() {
		b, r, err := c.repairTable(ctx, rung, t, through)
		buckets, rows = buckets+b, rows+r
		if err != nil {
			return buckets, rows, err
		}
	}
	return buckets, rows, nil
}

// repairTable walks the windows of one table that its source tier still holds
// whole. A window that starts before the oldest source bucket is skipped:
// retention cut into it, or the table began inside it.
func (c *Compactor) repairTable(ctx context.Context, rung Tier, t tierTable, through time.Time) (int, int, error) {
	oldest, ok, err := t.oldest(ctx, rung.Source)
	if err != nil || !ok {
		return 0, 0, err
	}
	start := tier.TruncateBucket(oldest, rung.Step)
	if start.Before(oldest) {
		start = tier.NextBucket(start, rung.Step)
	}
	var buckets, rows int
	for end := tier.NextBucket(start, rung.Step); !end.After(through); start, end = end, tier.NextBucket(end, rung.Step) {
		if err := ctx.Err(); err != nil {
			return buckets, rows, err
		}
		n, err := c.repairBucket(ctx, rung, t, start, end)
		if err != nil {
			return buckets, rows, err
		}
		if n > 0 {
			buckets++
			rows += n
		}
	}
	return buckets, rows, nil
}

// repairBucket builds one coarse bucket of t that holds no rows, when its
// source window is complete. It returns the rows written.
func (c *Compactor) repairBucket(ctx context.Context, rung Tier, t tierTable, start, end time.Time) (int, error) {
	have, err := t.count.CountSpanRollupBuckets(ctx, rung.Step, start, end)
	if err != nil || have > 0 {
		return 0, err
	}
	if ok, err := windowComplete(ctx, t.count, t.arrivals, rung.Source, start, end); err != nil || !ok {
		return 0, err
	}
	return t.build(ctx, rung, start, end)
}
