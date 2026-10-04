package retention

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// shardedRepo is a real repository whose shard files are faked: shards lists
// their sizes oldest first, and retiring one moves its bytes to pending.
type shardedRepo struct {
	*repository.Repository
	shards  []int64
	pending int64
	retired int
}

func (r *shardedRepo) RetireOldestShard(context.Context, time.Time, time.Time) (int64, bool, error) {
	if len(r.shards) == 0 {
		return 0, false, nil
	}
	freed := r.shards[0]
	r.shards = r.shards[1:]
	r.pending += freed
	r.retired++
	return freed, true, nil
}

func (r *shardedRepo) RetiringShardBytes(context.Context) (int64, error) { return r.pending, nil }

func newShardedWorker(t *testing.T, cfg Config, shards []int64) (*RetentionWorker, *shardedRepo) {
	t.Helper()
	_, real := setupDiskWorker(t, cfg)
	repo := &shardedRepo{Repository: real, shards: shards}
	logger := slog.Default()
	cfg.BatchYield = time.Millisecond
	return NewRetentionWorker(repo, aggregation.NewAggregator(real, time.Minute, logger), cfg, logger), repo
}

func TestReclaimRetiresOldestShardsBeforeEvictingSpans(t *testing.T) {
	cfg := Config{InterestingRetentionHours: 48, ErrorLogRetentionDays: 30, TargetFraction: 0.70}
	worker, repo := newShardedWorker(t, cfg, []int64{100, 100, 100, 100, 100})
	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatal(err)
	}
	insertTestSpans(t, repo.Repository, 3, "ok", 500, time.Now().UTC().Add(-2*time.Hour))

	// 95% used of 1000 bytes: three 100-byte shards bring it to 65%.
	space := repository.Space{PageSize: 4096, VolumeBytes: 1000, VolumeFreeBytes: 50}
	used, pending := worker.retireShardsForReclaim(context.Background(), worker.cfg, space, 0.70)
	if repo.retired != 3 || pending != 300 {
		t.Fatalf("retired %d shards with %d pending bytes, want 3 and 300", repo.retired, pending)
	}
	if used < 0.649 || used > 0.651 {
		t.Fatalf("projected usage %.3f, want 0.65", used)
	}
	res, err := worker.evictUntilUnderTarget(context.Background(), worker.cfg, repo, used, 0.70, pending)
	if err != nil {
		t.Fatal(err)
	}
	if res.evicted != 0 || res.rounds != 0 {
		t.Fatalf("evicted %d rows in %d rounds after retiring shards, want none", res.evicted, res.rounds)
	}
	var spans int
	if err := repo.DB().QueryRow(`SELECT count(*) FROM spans`).Scan(&spans); err != nil {
		t.Fatal(err)
	}
	if spans != 3 {
		t.Fatalf("%d spans left, want 3", spans)
	}
}

func TestReclaimCountsRetiringShardsAsFree(t *testing.T) {
	cfg := Config{InterestingRetentionHours: 48, ErrorLogRetentionDays: 30, TargetFraction: 0.70}
	worker, repo := newShardedWorker(t, cfg, []int64{100, 100})
	repo.pending = 300

	space := repository.Space{PageSize: 4096, VolumeBytes: 1000, VolumeFreeBytes: 50}
	used, _ := worker.retireShardsForReclaim(context.Background(), worker.cfg, space, 0.70)
	if repo.retired != 0 {
		t.Fatalf("retired %d more shards while 300 bytes were already on their way out", repo.retired)
	}
	if used > 0.70 {
		t.Fatalf("projected usage %.3f, want at most the target", used)
	}
}
