package repository

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
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

func shardRows(t *testing.T, path, file, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(ShardsDir(path), file)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s in %s: %v", table, file, err)
	}
	return n
}

func mainRows(t *testing.T, store *Storage, table string) int {
	t.Helper()
	var n int
	if err := store.Main.QueryRow(`SELECT count(*) FROM main.` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestShardInsertsCrossDayBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T23:59:30Z")
	store := openShardedStorage(t, path, &clock)
	repo := store.Repository()
	ctx := context.Background()

	insert := func() {
		if err := repo.InsertLogs(ctx, []model.LogRecord{makeLogRecord(1, "t1", "hello", 9)}); err != nil {
			t.Fatal(err)
		}
		if err := repo.InsertMetrics(ctx, []model.MetricRecord{makeMetricRecord(1, "cpu", model.MetricTypeGauge, 1)}); err != nil {
			t.Fatal(err)
		}
	}
	insert()
	clock = mustTime(t, "2026-10-05T00:00:30Z")
	insert()
	insert()

	for file, want := range map[string]int{"logs-20261004.db": 1, "logs-20261005.db": 2} {
		if n := shardRows(t, path, file, "logs"); n != want {
			t.Errorf("%s: %d logs, want %d", file, n, want)
		}
	}
	for file, want := range map[string]int{"metrics-20261004.db": 1, "metrics-20261005.db": 2} {
		if n := shardRows(t, path, file, "metrics"); n != want {
			t.Errorf("%s: %d metrics, want %d", file, n, want)
		}
	}
	if n := mainRows(t, store, "logs") + mainRows(t, store, "metrics"); n != 0 {
		t.Errorf("main got %d rows", n)
	}
}

func TestShardInsertsCrossWeekBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T23:59:00Z") // Sunday, ISO week 40
	store := openShardedStorage(t, path, &clock)
	repo := store.Repository()

	insert := func() {
		if err := repo.InsertPromptRecords([]PromptRecord{{ProjectID: 1, TraceID: "t", SpanID: "s", Name: "chat"}}); err != nil {
			t.Fatal(err)
		}
	}
	insert()
	clock = mustTime(t, "2026-10-05T00:01:00Z") // Monday, week 41
	insert()

	for _, file := range []string{"prompts-2026w40.db", "prompts-2026w41.db"} {
		if n := shardRows(t, path, file, "prompt_records"); n != 1 {
			t.Errorf("%s: %d prompt records, want 1", file, n)
		}
	}
	if n := mainRows(t, store, "prompt_records"); n != 0 {
		t.Errorf("main got %d prompt records", n)
	}
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

func TestShardRowsMatchFilesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T12:00:00Z")
	store := openShardedStorage(t, path, &clock)
	if err := store.Repository().InsertLogs(context.Background(), []model.LogRecord{makeLogRecord(1, "t", "x", 9)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Main.Exec(`INSERT INTO shards (family, period_start, period_end, file)
		VALUES ('logs', '2026-10-01', '2026-10-02', 'logs-20261001.db')`); err != nil {
		t.Fatal(err)
	}
	store.Close()
	// A file without a row: the writer stopped between creating and recording it.
	if err := os.WriteFile(filepath.Join(ShardsDir(path), "metrics-20261003.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	store = openShardedStorage(t, path, &clock)
	rows := shardFileRows(t, store)
	files, err := filepath.Glob(filepath.Join(ShardsDir(path), "*.db"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, f := range files {
		onDisk = append(onDisk, filepath.Base(f))
	}
	sort.Strings(onDisk)
	if !equalStrings(rows, onDisk) || len(rows) != 4 {
		t.Errorf("rows %v, files %v", rows, onDisk)
	}
	if n := shardRows(t, path, "metrics-20261003.db", "metrics"); n != 0 {
		t.Errorf("stray file not migrated: %d rows", n)
	}
	if n := shardRows(t, path, "logs-20261004.db", "logs"); n != 1 {
		t.Errorf("logs lost over restart: %d", n)
	}
}

func TestShardsOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store := openTestStorage(t, path)
	if store.Shards != nil || store.Shards.Sharded(FamilyLogs) {
		t.Fatal("shards on without Shards option")
	}
	if err := store.Repository().InsertLogs(context.Background(), []model.LogRecord{makeLogRecord(1, "t", "x", 9)}); err != nil {
		t.Fatal(err)
	}
	if n := mainRows(t, store, "logs"); n != 1 {
		t.Errorf("main has %d logs", n)
	}
	if _, err := os.Stat(ShardsDir(path)); !os.IsNotExist(err) {
		t.Errorf("shards dir created: %v", err)
	}
}

// Retention deletes of a sharded family keep running on main, which still
// holds the rows written before the shards.
func TestShardedFamilyDeletesStayOnMain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	old := openTestStorage(t, path)
	if err := old.Repository().InsertLogs(context.Background(), []model.LogRecord{makeLogRecord(1, "t", "old", 9)}); err != nil {
		t.Fatal(err)
	}
	old.Close()

	clock := time.Now().UTC()
	store := openShardedStorage(t, path, &clock)
	n, err := store.Repository().DeleteLogsOlderThan(context.Background(), time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || mainRows(t, store, "logs") != 0 {
		t.Errorf("deleted %d, main has %d", n, mainRows(t, store, "logs"))
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

func shardFileRows(t *testing.T, store *Storage) []string {
	t.Helper()
	rows, err := store.Main.Query(`SELECT file FROM shards ORDER BY file`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
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
