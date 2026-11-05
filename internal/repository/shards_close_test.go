package repository

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedLog collects log lines written from several goroutines.
type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// lines returns the lines that contain every one of parts.
func (l *lockedLog) lines(parts ...string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range strings.Split(l.b.String(), "\n") {
		match := line != ""
		for _, p := range parts {
			match = match && strings.Contains(line, p)
		}
		if match {
			out = append(out, line)
		}
	}
	return out
}

// heldEndedShard opens a sharded storage on 2026-10-04 with a short final
// checkpoint budget, and takes the single writer connection of that day's logs
// shard, so its final checkpoint cannot run until the connection is released.
func heldEndedShard(t *testing.T) (store *Storage, path string, log *lockedLog, held *sql.Conn) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T12:00:00Z")
	store = openShardedStorage(t, path, &clock)
	log = &lockedLog{}
	store.Shards.logger = slog.New(slog.NewTextHandler(log, nil))
	store.Shards.closeBudget = 200 * time.Millisecond
	store.Shards.mu.Lock()
	h := store.Shards.handles[shardKey{FamilyLogs, mustTime(t, "2026-10-04T00:00:00Z")}]
	store.Shards.mu.Unlock()
	if h == nil {
		t.Fatal("no logs shard for 2026-10-04")
	}
	held, err := h.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Close() })
	clock = mustTime(t, "2026-10-05T12:00:00Z")
	return store, path, log, held
}

const checkpointFailed = `msg="close ended shard: final checkpoint failed"`

// A final checkpoint that times out once is retried, and the retry that resets
// the WAL logs a warning and no error (#501, SPA-89). The retry gets the
// production budget: under the first attempt's 200 ms its TRUNCATE ran out of
// time on a loaded CI runner (658 ms on 2026-11-05).
func TestShardCloseRetriesFinalCheckpoint(t *testing.T) {
	store, path, log, held := heldEndedShard(t)
	store.Shards.beforeCheckpointRetry = func() {
		held.Close()
		store.Shards.closeBudget = endedCheckpointBudget
	}

	if err := store.Shards.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := log.lines("level=ERROR"); len(got) != 0 {
		t.Errorf("error logged for a checkpoint the retry recovered: %q", got)
	}
	if got := log.lines("level=WARN", "final checkpoint failed, retrying", "file=logs-20261004.db", "cause=timeout", "attempt=1"); len(got) != 1 {
		t.Errorf("want one warning for the timed out attempt, got %q", got)
	}
	if got := log.lines("level=WARN", "final checkpoint recovered on retry", "file=logs-20261004.db", "attempt=2"); len(got) != 1 {
		t.Errorf("want one warning for the recovery, got %q", got)
	}
	if fileExists(filepath.Join(ShardsDir(path), "logs-20261004.db-wal")) {
		t.Error("ended shard still has its WAL after the retry")
	}
}

// When the retry also leaves the WAL in place, the close logs one error that
// names the cause and where the time went.
func TestShardCloseFinalCheckpointFailsAfterRetry(t *testing.T) {
	store, _, log, _ := heldEndedShard(t)
	retries := 0
	store.Shards.beforeCheckpointRetry = func() { retries++ }

	if err := store.Shards.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if retries != 1 {
		t.Errorf("retries: got %d, want 1", retries)
	}
	if got := log.lines("level=WARN", "final checkpoint failed, retrying", "cause=timeout", "attempt=1"); len(got) != 1 {
		t.Errorf("want one warning before the retry, got %q", got)
	}
	got := log.lines("level=ERROR")
	if len(got) != 1 {
		t.Fatalf("want one error line, got %q", got)
	}
	for _, part := range []string{checkpointFailed, "file=logs-20261004.db", "cause=timeout", "attempt=2", "conn_wait_ms=", "truncate_ms=", "wal_frames=-1"} {
		if !strings.Contains(got[0], part) {
			t.Errorf("error line %q lacks %s", got[0], part)
		}
	}
}
