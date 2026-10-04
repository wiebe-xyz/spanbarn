package retention

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// shardExpirer is the part of the repository that expires time-shard files.
// Like spaceReporter it is optional, so a worker over a repository without
// shards runs the row deletes alone.
type shardExpirer interface {
	ExpireShards(ctx context.Context, now time.Time, c repository.ShardCutoffs) (repository.ShardExpiry, error)
}

// shardReclaimer is the part of the repository that gives up whole shard
// files under disk pressure.
type shardReclaimer interface {
	RetireOldestShard(ctx context.Context, now, errorLogCutoff time.Time) (int64, bool, error)
	RetiringShardBytes(ctx context.Context) (int64, error)
}

// expireShards retires the shard files past their family's window and
// deletes the ones retiring long enough. Rows the families wrote to main
// before their shards keep expiring through the row deletes.
func (w *RetentionWorker) expireShards(ctx context.Context, cut cycleCutoffs, st *cycleStats) error {
	e, ok := w.repo.(shardExpirer)
	if !ok {
		return nil
	}
	res, err := e.ExpireShards(ctx, cut.now, repository.ShardCutoffs{
		Logs:      cut.logs,
		Metrics:   cut.metrics,
		Prompts:   cut.prompts,
		ErrorLogs: cut.errorLogs,
	})
	st.shardsRetired, st.shardsDeleted, st.shardRowsTrimmed = res.Retired, res.Deleted, res.RowsTrimmed
	return err
}

// retireShardsForReclaim is reclaim's first step: retire the oldest shard
// files, across families, until the volume is projected under target once
// the retiring files are deleted. It returns that projected usage, and the
// bytes still waiting for deletion. A whole file costs a fraction of the
// time a row delete of the same data does, and frees its space without a
// vacuum.
func (w *RetentionWorker) retireShardsForReclaim(ctx context.Context, cfg Config, space repository.Space, target float64) (float64, int64) {
	used := space.UsedFraction()
	r, ok := w.repo.(shardReclaimer)
	if !ok || space.VolumeBytes <= 0 {
		return used, 0
	}
	pending, err := r.RetiringShardBytes(ctx)
	if err != nil {
		w.logger.Warn("retention: could not size retiring shards", "error", err)
		return used, 0
	}
	now := time.Now().UTC()
	errorLogCutoff := now.Add(-time.Duration(cfg.ErrorLogRetentionDays) * 24 * time.Hour)
	for used-float64(pending)/float64(space.VolumeBytes) > target {
		freed, ok, err := r.RetireOldestShard(ctx, now, errorLogCutoff)
		if err != nil {
			w.logger.Warn("retention: could not retire a shard", "error", err)
			break
		}
		if !ok {
			break
		}
		pending += freed
		w.logger.Warn("retention: retired the oldest shard to reclaim space", "freed_bytes", freed)
	}
	return used - float64(pending)/float64(space.VolumeBytes), pending
}
