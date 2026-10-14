package repository

import (
	"context"
	"testing"
	"time"
)

// With logs sharded, main's logs table is dropped once empty. The per-project
// log cap must not fail on the missing table, and has to trim the shard files
// where the rows now live, sparing pinned traces.
func TestDeleteProjectLogsOlderThanOnShardedLogs(t *testing.T) {
	path, store, repo, clock := expiryFixture(t)
	ctx := context.Background()
	const file = "logs-20261004.db"

	// The shard holds the rows; two expiry passes retire and then drop main's
	// emptied table, like production did.
	cut := ShardCutoffs{Logs: clock.Add(-30 * 24 * time.Hour), ErrorLogs: errorLogsSince}
	for _, at := range []time.Time{*clock, clock.Add(shardDeleteDelay + time.Hour)} {
		if _, err := repo.ExpireShards(ctx, at, cut); err != nil {
			t.Fatal(err)
		}
	}
	if has, err := hasTable(ctx, store.Main.DB, "logs"); err != nil || has {
		t.Fatalf("main still has logs (has=%v err=%v), the fixture does not reproduce the sharded layout", has, err)
	}

	n, err := repo.DeleteProjectLogsOlderThan(ctx, 1, mustTime(t, "2026-10-05T00:00:00Z"))
	if err != nil {
		t.Fatalf("DeleteProjectLogsOlderThan: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d logs, want 2 (the error-sampled and the plain trace)", n)
	}
	if got := shardRows(t, path, file, "logs"); got != 1 {
		t.Errorf("shard keeps %d logs, want only the pinned trace", got)
	}

	// Another project's logs and a cutoff before the rows leave them alone.
	if n, err := repo.DeleteProjectLogsOlderThan(ctx, 2, mustTime(t, "2026-10-05T00:00:00Z")); err != nil || n != 0 {
		t.Errorf("other project: deleted %d, err %v, want nothing", n, err)
	}
	if n, err := repo.DeleteProjectLogsOlderThan(ctx, 1, mustTime(t, "2026-10-03T00:00:00Z")); err != nil || n != 0 {
		t.Errorf("early cutoff: deleted %d, err %v, want nothing", n, err)
	}
}
