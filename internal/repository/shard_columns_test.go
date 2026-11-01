package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func addProbeColumn(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN rollback_probe TEXT DEFAULT 'set'`); err != nil {
		t.Fatalf("add column to %s: %v", table, err)
	}
}

func addProbeColumnToFile(t *testing.T, path, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	addProbeColumn(t, db, table)
}

// A shard migration adds a column to the shards that exist. After a rollback
// the older image creates the next shard without it, so the older shard is a
// migration ahead of the newest. Every read of the family has to keep working,
// and a later writer's migration of the short shard has to show without a
// restart.
func TestShardViewsReadShardsWithDifferentColumns(t *testing.T) {
	for _, tc := range []struct {
		family        Family
		table         string
		older, newest string
		rows          int
	}{
		{FamilyLogs, "logs", "logs-20261004.db", "logs-20261005.db", 3},
		{FamilyMetrics, "metrics", "metrics-20261004.db", "metrics-20261005.db", 3},
		{FamilyPrompts, "prompt_records", "prompts-2026w40.db", "prompts-2026w41.db", 3},
	} {
		t.Run(tc.table, func(t *testing.T) {
			path, _, _, rs, _ := shardedReadFixture(t)
			ctx := context.Background()
			read := func() (rows, probed int) {
				t.Helper()
				if err := rs.Refresh(ctx); err != nil {
					t.Fatalf("refresh: %v", err)
				}
				q := `SELECT count(*), count(rollback_probe) FROM ` + tc.table
				if err := rs.pool(tc.family).db.QueryRow(q).Scan(&rows, &probed); err != nil {
					t.Fatalf("read %s: %v", tc.table, err)
				}
				return rows, probed
			}

			addProbeColumnToFile(t, filepath.Join(ShardsDir(path), tc.older), tc.table)
			if rows, probed := read(); rows != tc.rows || probed != 1 {
				t.Errorf("one shard ahead: %d rows, %d with the column, want %d and 1", rows, probed, tc.rows)
			}

			addProbeColumnToFile(t, filepath.Join(ShardsDir(path), tc.newest), tc.table)
			if rows, probed := read(); rows != tc.rows || probed != 2 {
				t.Errorf("short shard migrated: %d rows, %d with the column, want %d and 2", rows, probed, tc.rows)
			}
		})
	}
}

// The repository's own reads run through the views and the per-segment
// queries, with the newest shard behind.
func TestShardReadsWorkWithAShardBehind(t *testing.T) {
	path, _, repo, rs, _ := shardedReadFixture(t)
	ctx := context.Background()
	shards := ShardsDir(path)
	addProbeColumnToFile(t, filepath.Join(shards, "logs-20261004.db"), "logs")
	addProbeColumnToFile(t, filepath.Join(shards, "prompts-2026w40.db"), "prompt_records")
	if err := rs.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	if _, total, err := repo.QueryLogs(ctx, LogFilter{ProjectID: 1, From: logsFrom, To: logsTo, Limit: 10}); err != nil || total != 3 {
		t.Errorf("logs: total %d, error %v, want 3", total, err)
	}
	if recs, err := repo.QueryPromptRecords(PromptFilter{ProjectID: 1, Limit: 10}); err != nil || len(recs) != 3 {
		t.Errorf("prompts: %d rows, error %v, want 3", len(recs), err)
	}
}

// The logs view also unions kept_logs, which follows the main track, so it can
// differ from the shards in either direction.
func TestLogsViewReadsKeptLogsWithDifferentColumns(t *testing.T) {
	_, store, _, rs, _ := shardedReadFixture(t)
	addProbeColumn(t, store.Main.DB, "kept_logs")
	if err := rs.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := rs.pool(FamilyLogs).db.QueryRow(`SELECT count(*) FROM logs WHERE rollback_probe IS NULL`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 3 {
		t.Errorf("%d shard rows read NULL for the kept_logs column, want 3", rows)
	}
}

// A shard a migration ahead of or behind kept_logs still gives up its protected
// rows and retires. A failed copy would hold it on the volume.
func TestKeepLogsCopiesAcrossDifferentColumns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(t *testing.T, shard string, store *Storage)
	}{
		{"shard ahead", func(t *testing.T, shard string, _ *Storage) { addProbeColumnToFile(t, shard, "logs") }},
		{"shard behind", func(t *testing.T, _ string, store *Storage) { addProbeColumn(t, store.Main.DB, "kept_logs") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, store, repo, clock := keepFixture(t)
			ctx := context.Background()
			tc.alter(t, filepath.Join(ShardsDir(path), "logs-20261004.db"), store)

			*clock = mustTime(t, "2026-10-06T01:00:00Z")
			if err := store.Shards.Maintain(ctx); err != nil {
				t.Fatal(err)
			}
			res, err := repo.ExpireShards(ctx, *clock, ShardCutoffs{Logs: clock.Add(-24 * time.Hour), ErrorLogs: errorLogsSince})
			if err != nil {
				t.Fatal(err)
			}
			if res.Retired != 1 {
				t.Fatalf("expiry %+v, want one shard retired", res)
			}
			if got := keptCount(t, store, "err"); got != retentionDeleteBatch+10 {
				t.Errorf("kept %d rows of err, want %d", got, retentionDeleteBatch+10)
			}
		})
	}
}
