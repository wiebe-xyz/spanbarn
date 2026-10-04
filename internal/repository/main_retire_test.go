package repository

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

var heavyTables = []string{"logs", "metrics", "prompt_records"}

func retiredTables(t *testing.T, store *Storage) int {
	t.Helper()
	var n int
	if err := store.Main.QueryRow(`SELECT count(*) FROM retired_tables`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mainHasTables(t *testing.T, store *Storage) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, table := range heavyTables {
		has, err := hasTable(context.Background(), store.Main.DB, table)
		if err != nil {
			t.Fatal(err)
		}
		out[table] = has
	}
	return out
}

func TestMainDropsEmptiedHeavyTables(t *testing.T) {
	_, store, reader, rs, clock := shardedReadFixture(t)
	ctx := context.Background()
	repo := store.Repository()
	expire := func() ShardExpiry {
		t.Helper()
		res, err := repo.ExpireShards(ctx, *clock, ShardCutoffs{})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	countLogs := func() int {
		t.Helper()
		if err := rs.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		_, n, err := reader.QueryLogs(ctx, LogFilter{ProjectID: 1, TraceID: "trace-x", From: logsFrom, To: logsTo, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Main still holds rows: nothing retires.
	expire()
	if n := retiredTables(t, store); n != 0 {
		t.Fatalf("%d tables retiring while main holds rows", n)
	}

	for _, table := range heavyTables {
		if _, err := store.Main.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
	expire()
	if n := retiredTables(t, store); n != 3 {
		t.Fatalf("%d tables retiring after main emptied, want 3", n)
	}
	if n := countLogs(); n != 2 {
		t.Fatalf("logs after retiring main's table: %d, want 2", n)
	}
	if p := rs.pool(FamilyLogs); p.mainHas {
		t.Fatal("the logs pool still reads main's retiring table")
	}

	*clock = clock.Add(4 * time.Minute)
	if res := expire(); res.MainTablesDropped != 0 {
		t.Fatalf("dropped %d tables before the delay", res.MainTablesDropped)
	}
	*clock = clock.Add(2 * time.Minute)
	if res := expire(); res.MainTablesDropped != 3 {
		t.Fatalf("dropped %d tables after the delay, want 3", res.MainTablesDropped)
	}
	for table, has := range mainHasTables(t, store) {
		if has {
			t.Errorf("main still has %s", table)
		}
	}
	if n := retiredTables(t, store); n != 0 {
		t.Fatalf("%d retired_tables rows after the drop", n)
	}

	// Row retention and the trace cascade skip the dropped tables.
	cutoff := clock.Add(-time.Hour)
	if _, err := repo.DeleteLogsOlderThan(ctx, cutoff, cutoff); err != nil {
		t.Fatalf("logs retention after the drop: %v", err)
	}
	if _, err := repo.DeleteMetricsOlderThan(ctx, cutoff); err != nil {
		t.Fatalf("metrics retention after the drop: %v", err)
	}
	if _, _, err := repo.DeletePromptRecordsOlderThanLimited(ctx, cutoff, 100); err != nil {
		t.Fatalf("prompts retention after the drop: %v", err)
	}
	if err := repo.deleteOtherTraceRows(ctx, []any{"trace-x"}); err != nil {
		t.Fatalf("trace cascade after the drop: %v", err)
	}
	if n := countLogs(); n != 2 {
		t.Fatalf("logs after the drop: %d, want 2", n)
	}
}

func TestMainRestoresHeavyTablesWithShardsOff(t *testing.T) {
	path, store, _, _, clock := shardedReadFixture(t)
	ctx := context.Background()
	for _, table := range heavyTables {
		if _, err := store.Main.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := store.Repository().ExpireShards(ctx, *clock, ShardCutoffs{}); err != nil {
			t.Fatal(err)
		}
		*clock = clock.Add(shardDeleteDelay)
	}
	if mainHasTables(t, store)["logs"] {
		t.Fatal("main kept its logs table")
	}
	store.Close()

	plain, err := OpenStorage(ctx, path, testStorageOptions)
	if err != nil {
		t.Fatalf("open without shards: %v", err)
	}
	defer plain.Close()
	for table, has := range mainHasTables(t, plain) {
		if !has {
			t.Errorf("main has no %s with shards off", table)
		}
	}
	if err := plain.Repository().InsertLogs(ctx, []model.LogRecord{makeLogRecord(1, "trace-y", "log", 9)}); err != nil {
		t.Fatalf("insert with shards off: %v", err)
	}
}
