// Package metricrollup compacts metric rollups into progressively coarser tiers.
//
// The 5-minute tier (table metric_rollups) is written by the ingest accumulator
// and keyed by the full attribute set. Everything coarser lives in
// metric_rollups_coarse, keyed additionally by step_seconds, and is produced
// here: each tier is built from the tier below it, one closed bucket at a time,
// oldest first.
//
// Two things make a tier smaller than its source: fewer buckets (twelve
// 5-minute buckets become one hour) and fewer series (volatile attributes such
// as service.version are dropped, so the deploy-scoped series merge into one).
// On production data the second effect is the larger one. The step widths and
// bucket arithmetic come from package tier.
package metricrollup

import (
	"context"
	"log/slog"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

// Repository is the data access the compactor needs.
type Repository interface {
	RollupWindow(ctx context.Context, step int64, from, to time.Time, limit int) ([]repository.MetricRollup, error)
	UpsertCoarseRollups(ctx context.Context, rollups []repository.MetricRollup) error
	CoarseLastBySeries(ctx context.Context, projectID, step int64, bucket time.Time) (map[string]float64, error)
	OldestRollupBucket(ctx context.Context, step int64) (time.Time, bool, error)
	MetricRollupWatermark(ctx context.Context, step int64) (time.Time, bool, error)
	SetMetricRollupWatermark(ctx context.Context, step int64, through time.Time) error
	GetSetting(key string) (string, error)
}

// spaceReporter is the optional capability that measures the volume. A
// repository that cannot measure space (an in-memory test database) simply runs
// without the free-space guard rather than failing.
type spaceReporter interface {
	DBSpace(ctx context.Context, dbPath string) (repository.Space, error)
}

// Config controls the compactor.
type Config struct {
	// DBPath is the database file, used to measure free space. Empty disables
	// the guard.
	DBPath string
	// Interval is how often a pass runs once the ladder is caught up.
	Interval time.Duration
	// BusyInterval is used while a pass still has backlog to work through, so a
	// month of un-compacted history drains in minutes rather than days.
	BusyInterval time.Duration
	// BucketsPerPass bounds how many target buckets each tier compacts per pass,
	// keeping a single write-lock hold short.
	BucketsPerPass int
	// MinFreeBytes is the filesystem headroom below which a pass stops. Each
	// bucket writes its coarse rows before retention drops the source window, so
	// a pass needs a little room even though the net effect frees space.
	MinFreeBytes int64
	// Disabled stops compaction entirely. The zero value runs, so a caller that
	// says nothing gets compaction; the flag exists so an operator can stop it
	// on a live system without rolling the binary back. Retention then holds
	// every tier, because a tier is only deleted as far as it was compacted.
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
	if c.MinFreeBytes <= 0 {
		c.MinFreeBytes = 50 << 20
	}
	return c
}

// Compactor rolls each tier of metric_rollups into the coarser tier above it.
//
// It is the only thing that bounds metric storage: retention deletes a tier only
// up to the watermark this writes, so history is never dropped before it has
// been summarised. It therefore has to keep working on a volume that is already
// full, which shapes the design — one closed bucket at a time, oldest first, so
// every pass is small, interruptible and idempotent.
type Compactor struct {
	repo   Repository
	cfg    Config
	logger *slog.Logger
	now    func() time.Time

	// lowSpace latches the free-space warning so a wedged volume does not file
	// an issue on every pass.
	lowSpace bool
	// busy reports that the last pass filled its budget, so there is more to do.
	busy bool
}

// New creates a Compactor.
func New(repo Repository, cfg Config, logger *slog.Logger) *Compactor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Compactor{repo: repo, cfg: cfg.withDefaults(), logger: logger, now: time.Now}
}

// Run compacts on a ticker until ctx is cancelled, quickly while there is
// backlog and slowly once the ladder is current.
func (c *Compactor) Run(ctx context.Context) {
	if c.cfg.Disabled {
		c.logger.Warn("rollup compaction is disabled — coarse tiers will not be written, " +
			"and retention will hold every tier because nothing advances the compaction watermark")
		return
	}

	timer := time.NewTimer(c.cfg.Interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("rollup compactor stopped")
			return
		case <-timer.C:
			if err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
				c.logger.Warn("rollup compaction pass failed", "error", err)
			}
			next := c.cfg.Interval
			if c.busy {
				next = c.cfg.BusyInterval
			}
			timer.Reset(next)
		}
	}
}

// RunOnce walks the ladder once, compacting up to BucketsPerPass target buckets
// per tier.
func (c *Compactor) RunOnce(ctx context.Context) error {
	if !c.haveRoom(ctx) {
		return nil
	}
	policy := c.policy()

	c.busy = false
	for _, rung := range tier.Ladder() {
		if err := ctx.Err(); err != nil {
			return err
		}
		done, rows, err := c.compactTier(ctx, rung, policy)
		if err != nil {
			return err
		}
		if done > 0 {
			c.logger.Info("rollup compaction",
				"tier", rung.Label, "buckets", done, "rows_written", rows)
		}
		if done >= c.cfg.BucketsPerPass {
			c.busy = true
		}
	}
	return nil
}

// compactTier advances one rung of the ladder, returning how many target buckets
// it compacted and how many coarse rows it wrote.
func (c *Compactor) compactTier(ctx context.Context, rung tier.Tier, policy DropPolicy) (int, int, error) {
	start, ok, err := c.tierStart(ctx, rung)
	if err != nil || !ok {
		return 0, 0, err
	}

	sourceThrough, sourceLimited, err := c.sourceThrough(ctx, rung)
	if err != nil {
		return 0, 0, err
	}

	var buckets, rows int
	for buckets < c.cfg.BucketsPerPass {
		if err := ctx.Err(); err != nil {
			return buckets, rows, err
		}
		// The wait covers the source bucket a flush has not written yet: the last
		// 5-minute bucket of an hour lands up to a flush interval after the hour
		// closes, or later when a failed write is retried, and the watermark
		// never comes back for it.
		if !tier.BucketClosed(start, rung.Step, c.now().Add(-repository.MetricRollupLateWrite)) {
			break // still taking writes
		}
		// The watermark never comes back for a bucket, so one compacted before the
		// tier below has caught up would miss those source rows for good.
		if sourceLimited && sourceThrough.Before(tier.TruncateBucket(tier.NextBucket(start, rung.Step), rung.Source)) {
			break
		}
		n, err := c.compactBucket(ctx, rung, start, policy)
		if err != nil {
			return buckets, rows, err
		}
		end := tier.NextBucket(start, rung.Step)
		if err := c.repo.SetMetricRollupWatermark(ctx, rung.Source, end); err != nil {
			return buckets, rows, err
		}
		rows += n
		buckets++
		start = end
	}
	return buckets, rows, nil
}

// sourceThrough reports how far this tier's source rows are complete: the
// watermark of the rung that writes them. Ingest writes the 5-minute tier, so the
// hourly tier has no such limit, and neither has a tier whose source was never
// compacted, because nothing is on its way to that source.
func (c *Compactor) sourceThrough(ctx context.Context, rung tier.Tier) (time.Time, bool, error) {
	for _, below := range tier.Ladder() {
		if below.Step == rung.Source {
			return c.repo.MetricRollupWatermark(ctx, below.Source)
		}
	}
	return time.Time{}, false, nil
}

// tierStart is the first target bucket this tier still owes: the watermark where
// one exists, otherwise the oldest source bucket. False means the source tier is
// empty and there is nothing to do.
func (c *Compactor) tierStart(ctx context.Context, rung tier.Tier) (time.Time, bool, error) {
	if through, ok, err := c.repo.MetricRollupWatermark(ctx, rung.Source); err != nil {
		return time.Time{}, false, err
	} else if ok {
		return tier.TruncateBucket(through, rung.Step), true, nil
	}

	oldest, ok, err := c.repo.OldestRollupBucket(ctx, rung.Source)
	if err != nil || !ok {
		return time.Time{}, false, err
	}
	return tier.TruncateBucket(oldest, rung.Step), true, nil
}

// compactBucket builds one target bucket from its source rows and returns how
// many coarse rows it wrote.
//
// The read starts one source bucket early. That extra row is what a cumulative
// 5-minute series needs to yield the increase inside the window; it takes part in
// the arithmetic without contributing to the bucket itself. A coarse source holds
// its own increases and ignores it.
func (c *Compactor) compactBucket(ctx context.Context, rung tier.Tier, bucket time.Time, policy DropPolicy) (int, error) {
	from, to := sourceWindow(rung, bucket)
	rows, err := c.repo.RollupWindow(ctx, rung.Source, tier.PrevBucket(from, rung.Source), to, 0)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	tails, byProject := splitWindow(rows, from)
	var written int
	for _, pid := range byProject.order {
		targetLast, err := c.repo.CoarseLastBySeries(ctx, pid, rung.Step, tier.PrevBucket(bucket, rung.Step))
		if err != nil {
			return written, err
		}
		merged := Merge(rung, bucket, pid, byProject.rows[pid], policy, Carry{
			SourceTail: tails,
			TargetLast: targetLast,
		})
		if len(merged) == 0 {
			continue
		}
		if err := c.repo.UpsertCoarseRollups(ctx, merged); err != nil {
			return written, err
		}
		written += len(merged)
	}
	return written, nil
}

// projectRows groups a window's rows by project, preserving first-seen order so
// a pass is deterministic.
type projectRows struct {
	order []int64
	rows  map[int64][]repository.MetricRollup
}

// splitWindow separates the carry rows (before the source window) from the rows
// being compacted, keeping the newest carry row per source series.
func splitWindow(rows []repository.MetricRollup, from time.Time) (map[string]repository.MetricRollup, projectRows) {
	tails := map[string]repository.MetricRollup{}
	out := projectRows{rows: map[int64][]repository.MetricRollup{}}

	for _, r := range rows {
		if r.Bucket.Before(from) {
			k := SourceTailKey(r)
			if prev, ok := tails[k]; !ok || r.Bucket.After(prev.Bucket) {
				tails[k] = r
			}
			continue
		}
		if _, seen := out.rows[r.ProjectID]; !seen {
			out.order = append(out.order, r.ProjectID)
		}
		out.rows[r.ProjectID] = append(out.rows[r.ProjectID], r)
	}
	return tails, out
}

// policy reads the attribute reduction, letting the settings table override the
// built-in lists globally.
func (c *Compactor) policy() DropPolicy {
	p := DefaultDropPolicy()
	if v, err := c.repo.GetSetting("metrics.rollup_drop_attrs.hourly"); err == nil && v != "" {
		p.Hourly = ParseDropList(v)
	}
	if v, err := c.repo.GetSetting("metrics.rollup_drop_attrs.daily"); err == nil && v != "" {
		p.Daily = ParseDropList(v)
	}
	return p
}

// haveRoom reports whether the volume has enough headroom to write this pass.
// Unmeasurable space counts as room: a broken probe must not stop the one
// process that frees the disk.
func (c *Compactor) haveRoom(ctx context.Context) bool {
	reporter, ok := c.repo.(spaceReporter)
	if !ok || c.cfg.DBPath == "" {
		return true
	}
	space, err := reporter.DBSpace(ctx, c.cfg.DBPath)
	if err != nil || !space.Measured() {
		return true
	}
	// Freelist pages are room to write, even though the file has not shrunk.
	if space.AvailableBytes() >= c.cfg.MinFreeBytes {
		if c.lowSpace {
			c.logger.Info("rollup compaction resumed — volume has room again",
				"available_bytes", space.AvailableBytes())
			c.lowSpace = false
		}
		return true
	}
	if !c.lowSpace {
		c.logger.Error("rollup compaction paused — volume has no room to write the coarse tier",
			"available_bytes", space.AvailableBytes(), "min_free_bytes", c.cfg.MinFreeBytes)
		c.lowSpace = true
	}
	return false
}

// sourceWindow returns the source buckets [from, to) that make up the target
// bucket of this tier starting at bucket.
//
// Every tier nests inside the one above it except weeks in months. A week that
// straddles two months belongs to the month it ends in: the earlier month closes
// before that week does, and waiting for the week would hold the month back for
// up to six days. Each week so lands in exactly one month, and every week of a
// month has closed by the time the month does.
func sourceWindow(rung tier.Tier, bucket time.Time) (from, to time.Time) {
	return tier.TruncateBucket(bucket, rung.Source), tier.TruncateBucket(tier.NextBucket(bucket, rung.Step), rung.Source)
}
