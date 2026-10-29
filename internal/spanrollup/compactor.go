// Package spanrollup compacts span_rollups, and span_edges with it, from
// 5-minute buckets into hours and hours into days.
//
// Each coarse bucket is built from every source row of its window and written
// whole, then the source tier's watermark moves past the window. A window
// with a source bucket that holds no rows while spans arrived in it is a gap
// (see Complete), and its coarse bucket is not written, so readers see the
// gap in the coarse tier too. A repair pass builds such a bucket later, once
// its window counts as complete. A pass that
// stops between the two writes the same rows again on the next pass, so the
// counts stay right. Retention deletes a tier only up to its watermark.
package spanrollup

import (
	"context"
	"log/slog"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/rollup"
)

// Repository is the data access the compactor needs.
type Repository interface {
	SpanRollupWindow(ctx context.Context, step int64, from, to time.Time) ([]repository.SpanRollup, error)
	ReplaceSpanRollups(ctx context.Context, rows []repository.SpanRollup) error
	OldestSpanRollupBucket(ctx context.Context, step int64) (time.Time, bool, error)
	SpanRollupWatermark(ctx context.Context, step int64) (time.Time, bool, error)
	SetSpanRollupWatermark(ctx context.Context, step int64, through time.Time) error
	BucketCounter
	SpanArrivals
	OldestSpanEdgeBucket(ctx context.Context, step int64) (time.Time, bool, error)
	SpanEdgeWindow(ctx context.Context, step int64, from, to time.Time) ([]repository.SpanEdge, error)
	ReplaceSpanEdges(ctx context.Context, rows []repository.SpanEdge) error
	CountSpanEdgeBuckets(ctx context.Context, step int64, from, to time.Time) (int, error)
}

// Tier is one rung of the ladder: buckets of Step seconds built from the tier
// of Source seconds.
type Tier struct {
	Step   int64
	Source int64
	Label  string
}

// Ladder is the compaction order, finest first.
func Ladder() []Tier {
	return []Tier{
		{Step: rollup.StepHour, Source: rollup.Step5m, Label: "hourly"},
		{Step: rollup.StepDay, Source: rollup.StepHour, Label: "daily"},
	}
}

// Config controls the compactor.
type Config struct {
	// Interval is how often a pass runs once the ladder is caught up.
	// Default 1m.
	Interval time.Duration
	// BusyInterval is the pause between passes while there is backlog.
	// Default 5s.
	BusyInterval time.Duration
	// BucketsPerPass bounds the target buckets one tier compacts per pass.
	// Default 12.
	BucketsPerPass int
	// Lag is how long after a 5-minute bucket ends it can still take writes:
	// spans wait in staging for their trace, and a failed flush retries. An
	// hour is compacted once it ended Lag ago. Default 10m.
	Lag time.Duration
	// Disabled stops compaction. Retention then keeps every tier, because a
	// tier is deleted only as far as it was compacted.
	Disabled bool
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = time.Minute
	}
	if c.BusyInterval <= 0 {
		c.BusyInterval = 5 * time.Second
	}
	if c.BucketsPerPass <= 0 {
		c.BucketsPerPass = 12
	}
	if c.Lag <= 0 {
		c.Lag = 10 * time.Minute
	}
	return c
}

// Compactor rolls each tier of span_rollups into the tier above it.
type Compactor struct {
	repo   Repository
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
	busy   bool
	// repaired is when the last repair pass ran.
	repaired time.Time
}

// New creates a Compactor.
func New(repo Repository, cfg Config, logger *slog.Logger) *Compactor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Compactor{repo: repo, cfg: cfg.withDefaults(), logger: logger, now: time.Now}
}

// Run compacts until ctx is cancelled.
func (c *Compactor) Run(ctx context.Context) {
	if c.cfg.Disabled {
		c.logger.Warn("span rollup compaction is disabled: hourly and daily span rollups are not written, " +
			"and retention keeps every tier")
		return
	}
	timer := time.NewTimer(c.cfg.Interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
				c.logger.Warn("span rollup compaction pass failed", "error", err)
			}
			next := c.cfg.Interval
			if c.busy {
				next = c.cfg.BusyInterval
			}
			timer.Reset(next)
		}
	}
}

// RunOnce walks the ladder once.
func (c *Compactor) RunOnce(ctx context.Context) error {
	c.busy = false
	for _, tier := range Ladder() {
		done, rows, err := c.compactTier(ctx, tier)
		if err != nil {
			return err
		}
		if done > 0 {
			c.logger.Info("span rollup compaction", "tier", tier.Label, "buckets", done, "rows_written", rows)
		}
		if done >= c.cfg.BucketsPerPass {
			c.busy = true
		}
	}
	return c.repairIfDue(ctx)
}

func (c *Compactor) compactTier(ctx context.Context, tier Tier) (int, int, error) {
	start, ok, err := c.tierStart(ctx, tier)
	if err != nil || !ok {
		return 0, 0, err
	}
	ready, err := c.readyThrough(ctx, tier)
	if err != nil {
		return 0, 0, err
	}
	var buckets, rows int
	for buckets < c.cfg.BucketsPerPass {
		if err := ctx.Err(); err != nil {
			return buckets, rows, err
		}
		end := rollup.NextBucket(start, tier.Step)
		if end.After(ready) {
			break
		}
		n, err := c.compactBucket(ctx, tier, start, end)
		if err != nil {
			return buckets, rows, err
		}
		if err := c.repo.SetSpanRollupWatermark(ctx, tier.Source, end); err != nil {
			return buckets, rows, err
		}
		rows += n
		buckets++
		start = end
	}
	return buckets, rows, nil
}

// readyThrough is how far a tier's source is complete. The 5-minute tier is
// complete up to Lag ago; a coarser source is complete as far as it was
// itself compacted.
func (c *Compactor) readyThrough(ctx context.Context, tier Tier) (time.Time, error) {
	if tier.Source == rollup.Step5m {
		return c.now().UTC().Add(-c.cfg.Lag), nil
	}
	below, ok, err := c.repo.SpanRollupWatermark(ctx, rollup.Step5m)
	if err != nil || !ok {
		return time.Time{}, err
	}
	return below, nil
}

// tierStart is the first target bucket the tier still owes.
func (c *Compactor) tierStart(ctx context.Context, tier Tier) (time.Time, bool, error) {
	if through, ok, err := c.repo.SpanRollupWatermark(ctx, tier.Source); err != nil || ok {
		return rollup.TruncateBucket(through, tier.Step), ok, err
	}
	oldest, ok, err := c.repo.OldestSpanRollupBucket(ctx, tier.Source)
	if err != nil || !ok {
		return time.Time{}, false, err
	}
	return rollup.TruncateBucket(oldest, tier.Step), true, nil
}

// compactBucket builds one coarse bucket of span_rollups and of span_edges.
// Each table checks its own source window for gaps: span_edges started later
// than span_rollups, so an hour can be whole in one and not in the other.
func (c *Compactor) compactBucket(ctx context.Context, tier Tier, start, end time.Time) (int, error) {
	n, err := c.compactRollupBucket(ctx, tier, start, end)
	if err != nil {
		return n, err
	}
	m, err := c.compactEdgeBucket(ctx, tier, start, end)
	return n + m, err
}

func (c *Compactor) compactRollupBucket(ctx context.Context, tier Tier, start, end time.Time) (int, error) {
	if ok, err := windowComplete(ctx, c.repo, c.repo, tier.Source, start, end); err != nil || !ok {
		if err == nil {
			c.logger.Info("span rollup compaction left a bucket out: a source bucket holds no rows",
				"tier", tier.Label, "bucket", start)
		}
		return 0, err
	}
	src, err := c.repo.SpanRollupWindow(ctx, tier.Source, start, end)
	if err != nil || len(src) == 0 {
		return 0, err
	}
	merged := Merge(src, tier.Step, start)
	if err := c.repo.ReplaceSpanRollups(ctx, merged); err != nil {
		return 0, err
	}
	return len(merged), nil
}

func (c *Compactor) compactEdgeBucket(ctx context.Context, tier Tier, start, end time.Time) (int, error) {
	if ok, err := windowComplete(ctx, BucketCounterFunc(c.repo.CountSpanEdgeBuckets), Arrivals(c.repo, c.repo), tier.Source, start, end); err != nil || !ok {
		if err == nil {
			c.logger.Info("span edge compaction left a bucket out: a source bucket holds no rows",
				"tier", tier.Label, "bucket", start)
		}
		return 0, err
	}
	src, err := c.repo.SpanEdgeWindow(ctx, tier.Source, start, end)
	if err != nil || len(src) == 0 {
		return 0, err
	}
	merged := MergeEdges(src, tier.Step, start)
	if err := c.repo.ReplaceSpanEdges(ctx, merged); err != nil {
		return 0, err
	}
	return len(merged), nil
}
