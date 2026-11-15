package engine

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

var testShardRetention = ShardRetention{
	FamilyLogs:    24 * time.Hour,
	FamilyMetrics: 7 * day,
	FamilyPrompts: 30 * day,
}

// openShardedStorage opens a storage with shards on, its clock at *clock.
func openShardedStorage(t *testing.T, path string, clock *time.Time) *Storage {
	t.Helper()
	o := testStorageOptions
	o.Shards = testShardRetention
	o.Now = func() time.Time { return *clock }
	store, err := OpenStorage(context.Background(), path, o)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestShardMaintainCreatesAheadAndClosesEnded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T22:30:00Z")
	store := openShardedStorage(t, path, &clock)
	ctx := context.Background()

	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(ShardsDir(path), "logs-20261005.db")) {
		t.Fatal("next day created more than shardLead ahead")
	}
	clock = mustTime(t, "2026-10-04T23:30:00Z")
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"logs-20261005.db", "metrics-20261005.db"} {
		if !fileExists(filepath.Join(ShardsDir(path), file)) {
			t.Errorf("%s not created ahead of midnight", file)
		}
	}
	if !fileExists(filepath.Join(ShardsDir(path), "prompts-2026w41.db")) {
		t.Error("next week not created ahead of Monday")
	}

	clock = mustTime(t, "2026-10-05T00:10:00Z")
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := openShardFiles(store.Shards); !equalStrings(got, []string{"logs-20261005.db", "metrics-20261005.db", "prompts-2026w41.db"}) {
		t.Errorf("open shards after midnight: %v", got)
	}
}

// An ended shard stays open through the first minutes of the next period,
// when the checkpoints of every other file are slowest, and closes after
// endedCloseDelay (#501).
func TestShardCloseWaitsPastMidnightBurst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T23:30:00Z")
	store := openShardedStorage(t, path, &clock)
	ctx := context.Background()
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}

	clock = mustTime(t, "2026-10-05T00:02:00Z")
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	open := openShardFiles(store.Shards)
	for _, file := range []string{"logs-20261004.db", "metrics-20261004.db", "prompts-2026w40.db"} {
		if !slices.Contains(open, file) {
			t.Errorf("%s closed 2 minutes after its period ended; open: %v", file, open)
		}
	}

	clock = mustTime(t, "2026-10-05T00:09:59Z")
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if open := openShardFiles(store.Shards); !slices.Contains(open, "logs-20261004.db") {
		t.Errorf("logs-20261004.db closed before endedCloseDelay; open: %v", open)
	}

	clock = mustTime(t, "2026-10-05T00:10:00Z")
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := openShardFiles(store.Shards); !equalStrings(got, []string{"logs-20261005.db", "metrics-20261005.db", "prompts-2026w41.db"}) {
		t.Errorf("open shards after endedCloseDelay: %v", got)
	}
}

// Closing an ended shard goes through its family's queue.
func TestShardCloseEndedThroughQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T12:00:00Z")
	store := openShardedStorage(t, path, &clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	store.Shards.SetScheduler(FamilyLogs, s)
	go s.Run(ctx)

	clock = mustTime(t, "2026-10-05T12:00:00Z")
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !fileExists(filepath.Join(ShardsDir(path), "logs-20261004.db-wal")) })
	if got := openShardFiles(store.Shards); !equalStrings(got, []string{"logs-20261005.db", "metrics-20261005.db", "prompts-2026w41.db"}) {
		t.Errorf("open shards: %v", got)
	}
}

// The close of an ended shard runs outside the family's write slot: a write
// queued while the close is still running goes ahead of it, and closing the
// storage waits for the close to finish. At the 2026-10-25 day rollover the
// close held the slot 36 to 45s.
func TestShardCloseEndedOutsideWriteSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T12:00:00Z")
	store := openShardedStorage(t, path, &clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	store.Shards.SetScheduler(FamilyLogs, s)
	go s.Run(ctx)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	store.Shards.beforeClose = func() {
		entered <- struct{}{}
		<-release
	}

	clock = mustTime(t, "2026-10-05T12:00:00Z")
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the ended shard's close did not start")
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if err := s.Submit(wctx, writescheduler.Low, "write", func() error { return nil }); err != nil {
		t.Fatalf("write queued during the close: %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	select {
	case <-closed:
		t.Fatal("storage closed while an ended shard was still closing")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatalf("close storage: %v", err)
	}
	if fileExists(filepath.Join(ShardsDir(path), "logs-20261004.db-wal")) {
		t.Error("ended shard still has its WAL after the storage closed")
	}
}

func openShardFiles(m *ShardManager) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for key := range m.handles {
		out = append(out, m.specs[key.family].file(key.start))
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in 10s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
