package repository

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// expiryFixture writes logs of an error-sampled, a pinned and a plain trace
// into the 4 October logs shard, and returns the storage, its repository and
// the clock.
func expiryFixture(t *testing.T) (string, *Storage, *Repository, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T10:00:00Z")
	store := openShardedStorage(t, path, &clock)
	repo := store.Repository()
	ctx := context.Background()
	var recs []model.LogRecord
	for _, trace := range []string{"err", "pin", "plain"} {
		recs = append(recs, makeLogRecord(1, trace, "log "+trace, 9))
	}
	if err := repo.InsertLogs(ctx, recs); err != nil {
		t.Fatal(err)
	}
	stampFile(t, filepath.Join(ShardsDir(path), "logs-20261004.db"), "logs", "2026-10-04 10:00:00")
	if err := repo.InsertErrorSamples([]Span{{ProjectID: 1, TraceID: "err", SpanID: "s1", Name: "op", Status: "error", Attributes: "{}", Events: "[]"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PinTrace(ctx, 1, "pin", "kept"); err != nil {
		t.Fatal(err)
	}
	return path, store, repo, &clock
}

var errorLogsSince = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func keptTraces(t *testing.T, store *Storage) []string {
	t.Helper()
	rows, err := store.Main.Query(`SELECT trace_id FROM kept_logs ORDER BY trace_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func shardState(t *testing.T, store *Storage, file string) string {
	t.Helper()
	var state string
	err := store.Main.QueryRow(`SELECT state FROM shards WHERE file = ?`, file).Scan(&state)
	if err != nil {
		return "gone"
	}
	return state
}

func TestShardExpiryKeepsErrorAndPinnedLogs(t *testing.T) {
	path, store, repo, clock := expiryFixture(t)
	ctx := context.Background()
	const file = "logs-20261004.db"

	*clock = mustTime(t, "2026-10-06T01:00:00Z")
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	cut := ShardCutoffs{Logs: clock.Add(-24 * time.Hour), ErrorLogs: errorLogsSince}
	res, err := repo.ExpireShards(ctx, *clock, cut)
	if err != nil {
		t.Fatal(err)
	}
	if res.Retired != 1 || res.Deleted != 0 {
		t.Fatalf("expiry %+v, want one shard retired and none deleted", res)
	}
	if got := keptTraces(t, store); !slices.Equal(got, []string{"err", "pin"}) {
		t.Fatalf("kept logs of %v, want [err pin]", got)
	}
	if s := shardState(t, store, file); s != shardRetiring {
		t.Fatalf("%s is %s, want retiring", file, s)
	}

	// Readers drop the retiring shard and read the kept logs instead.
	ro, err := OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	reader := NewReadOnlyRepository(ro.DB)
	rs := NewShardReaders(ro.DB, path, 2, 0, nil)
	defer rs.Close()
	reader.SetShardReaders(rs)
	if err := rs.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if p := rs.pool(FamilyLogs); p == nil || slices.Contains(p.files, file) {
		t.Fatalf("logs pool after retiring: %+v", p)
	}
	for trace, want := range map[string]int{"err": 1, "pin": 1, "plain": 0} {
		_, n, err := reader.QueryLogs(ctx, LogFilter{ProjectID: 1, TraceID: trace, From: errorLogsSince, To: clock.Add(time.Hour), Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("logs of %s after expiry: %d, want %d", trace, n, want)
		}
	}

	// A second pass copies nothing twice and deletes nothing early.
	*clock = clock.Add(4 * time.Minute)
	if res, err = repo.ExpireShards(ctx, *clock, cut); err != nil || res.Deleted != 0 {
		t.Fatalf("expiry four minutes in: %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(ShardsDir(path), file)); err != nil {
		t.Fatalf("file deleted before the delay: %v", err)
	}
	if got := keptTraces(t, store); len(got) != 2 {
		t.Fatalf("kept logs after a second pass: %v", got)
	}

	*clock = clock.Add(2 * time.Minute)
	if res, err = repo.ExpireShards(ctx, *clock, cut); err != nil || res.Deleted != 1 {
		t.Fatalf("expiry six minutes in: %+v, %v", res, err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(filepath.Join(ShardsDir(path), file+suffix)); !os.IsNotExist(err) {
			t.Errorf("%s%s still on disk: %v", file, suffix, err)
		}
	}
	if s := shardState(t, store, file); s != "gone" {
		t.Fatalf("row of %s is %s after deletion", file, s)
	}
}

func TestShardExpiryNeverRetiresTheCurrentShard(t *testing.T) {
	_, store, repo, clock := expiryFixture(t)
	ctx := context.Background()
	// A cutoff past the end of today's shard, as a misconfigured window
	// would give.
	res, err := repo.ExpireShards(ctx, *clock, ShardCutoffs{Logs: clock.Add(48 * time.Hour), ErrorLogs: errorLogsSince})
	if err != nil {
		t.Fatal(err)
	}
	if res.Retired != 0 {
		t.Fatalf("retired %d shards that inserts still reach", res.Retired)
	}
	if s := shardState(t, store, "logs-20261004.db"); s != shardActive {
		t.Fatalf("current shard is %s", s)
	}
}

func TestShardExpiryTrimsInsideTheShardForShortWindows(t *testing.T) {
	path, store, repo, clock := expiryFixture(t)
	ctx := context.Background()
	const file = "logs-20261004.db"
	stampFile(t, filepath.Join(ShardsDir(path), file), "logs", "2026-10-04 08:00:00")
	if err := repo.InsertLogs(ctx, []model.LogRecord{makeLogRecord(1, "late", "log late", 9)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Shards.handles[shardKey{FamilyLogs, mustTime(t, "2026-10-04T00:00:00Z")}].Exec(
		`UPDATE logs SET ingested_at = '2026-10-04 20:00:00' WHERE trace_id = 'late'`); err != nil {
		t.Fatal(err)
	}

	// The elevated tier halves the 24h window: 12h is shorter than the
	// shard's day, so the rows before the cutoff go inside the shard.
	*clock = mustTime(t, "2026-10-04T23:00:00Z")
	res, err := repo.ExpireShards(ctx, *clock, ShardCutoffs{Logs: clock.Add(-12 * time.Hour), ErrorLogs: errorLogsSince})
	if err != nil {
		t.Fatal(err)
	}
	if res.RowsTrimmed != 3 || res.Retired != 0 {
		t.Fatalf("expiry %+v, want 3 rows trimmed and no shard retired", res)
	}
	if n := shardRows(t, path, file, "logs"); n != 1 {
		t.Fatalf("%d logs left in the shard, want the late one", n)
	}
	if got := keptTraces(t, store); !slices.Equal(got, []string{"err", "pin"}) {
		t.Fatalf("kept logs of %v, want [err pin]", got)
	}

	// The configured 24h window is a whole period: no trimming.
	res, err = repo.ExpireShards(ctx, *clock, ShardCutoffs{Logs: clock.Add(-24 * time.Hour), ErrorLogs: errorLogsSince})
	if err != nil || res.RowsTrimmed != 0 {
		t.Fatalf("24h window: %+v, %v", res, err)
	}
}

func TestRetireOldestShardAcrossFamilies(t *testing.T) {
	_, store, repo, clock := expiryFixture(t)
	ctx := context.Background()
	if err := repo.InsertMetrics(ctx, []model.MetricRecord{makeMetricRecord(1, "cpu", model.MetricTypeGauge, 1)}); err != nil {
		t.Fatal(err)
	}
	*clock = mustTime(t, "2026-10-05T10:00:00Z")
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}

	// Every shard of a period before today's goes: last week's prompts shard
	// (5 October is a Monday) and 4 October's logs and metrics. Today's
	// shards still take inserts.
	var retired []string
	for {
		freed, ok, err := repo.RetireOldestShard(ctx, *clock, errorLogsSince)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if freed <= 0 {
			t.Fatalf("retired a shard that frees %d bytes", freed)
		}
		rows, _ := store.Shards.rows(ctx)
		for _, r := range rows {
			if r.state == shardRetiring && !slices.Contains(retired, r.file) {
				retired = append(retired, r.file)
			}
		}
	}
	if want := []string{"prompts-2026w40.db", "logs-20261004.db", "metrics-20261004.db"}; !slices.Equal(retired, want) {
		t.Fatalf("retired %v, want %v", retired, want)
	}
	pending, err := repo.RetiringShardBytes(ctx)
	if err != nil || pending <= 0 {
		t.Fatalf("retiring bytes %d, %v", pending, err)
	}
}

func TestDBSpaceCountsShardFiles(t *testing.T) {
	path, store, repo, _ := expiryFixture(t)
	without, err := NewRepository(store.Main.DB).DBSpace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	with, err := repo.DBSpace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if with.FileBytes <= without.FileBytes {
		t.Fatalf("DBSpace with shards counts %d file bytes, without %d", with.FileBytes, without.FileBytes)
	}
}
