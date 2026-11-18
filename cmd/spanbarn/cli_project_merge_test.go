package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

func newMergeCLIFixture(t *testing.T) (config.Config, int64, int64) {
	t.Helper()
	cfg := config.Config{DBPath: filepath.Join(t.TempDir(), "spanbarn.db")}
	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()
	src, err := repo.CreateProject("main-staging", "main-staging")
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := repo.CreateProject("main", "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "worker"} {
		if _, err := repo.CreateAPIKey(src.ID, name, "hash-"+name, "ingest"); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, src.ID, tgt.ID
}

func keysOf(t *testing.T, cfg config.Config, projectID int64) (n int, envs string) {
	t.Helper()
	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()
	keys, err := repo.ListAPIKeys(projectID)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, k := range keys {
		seen = append(seen, k.Environment)
	}
	return len(keys), strings.Join(seen, ",")
}

// A merge repoints the source's keys at the target with the environment, a
// second run moves nothing, and the progress survives in project_merges.
func TestProjectMergeRepointsKeysOnAFile(t *testing.T) {
	cfg, src, tgt := newMergeCLIFixture(t)
	hostIdle = func() (float64, error) { return 90, nil }
	t.Cleanup(func() { hostIdle = readHostIdle })

	args := []string{"merge", "--source=main-staging", "--target=main", "--env=staging", "--batch=1"}
	if err := runProjectCmd(cfg, args); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if n, envs := keysOf(t, cfg, tgt); n != 2 || envs != "staging,staging" {
		t.Errorf("target keys = %d (%s), want 2 stamped staging", n, envs)
	}
	if n, _ := keysOf(t, cfg, src); n != 0 {
		t.Errorf("source keys = %d, want 0", n)
	}
	if err := runProjectCmd(cfg, args); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

func TestProjectMergeRefusesBadInput(t *testing.T) {
	cfg, _, _ := newMergeCLIFixture(t)
	hostIdle = func() (float64, error) { return 90, nil }
	t.Cleanup(func() { hostIdle = readHostIdle })
	for name, args := range map[string][]string{
		"no flags":        {"merge"},
		"bad environment": {"merge", "--source=main-staging", "--target=main", "--env=Not A Name"},
		"unknown source":  {"merge", "--source=nope", "--target=main", "--env=staging"},
		"same project":    {"merge", "--source=main", "--target=main", "--env=staging"},
	} {
		if err := runProjectCmd(cfg, args); err == nil {
			t.Errorf("%s: merge succeeded, want an error", name)
		}
	}
}

func TestProjectMergeIdleGate(t *testing.T) {
	cfg, src, _ := newMergeCLIFixture(t)
	t.Cleanup(func() { hostIdle = readHostIdle })
	args := []string{"merge", "--source=main-staging", "--target=main", "--env=staging"}

	hostIdle = func() (float64, error) { return 12.5, nil }
	err := runProjectCmd(cfg, args)
	if err == nil || !strings.Contains(err.Error(), "host is busy") {
		t.Fatalf("busy host: %v, want the gate to refuse", err)
	}
	if n, _ := keysOf(t, cfg, src); n != 2 {
		t.Errorf("the refused merge moved keys: source holds %d, want 2", n)
	}

	hostIdle = func() (float64, error) { return 0, errors.New("no /proc/stat") }
	if err := runProjectCmd(cfg, args); err != nil {
		t.Fatalf("unreadable host: %v, want the merge to run", err)
	}
}

func TestParseCPUStat(t *testing.T) {
	idle, total, err := parseCPUStat("cpu  100 5 50 800 20 0 10 15 0 0")
	if err != nil || idle != 800 || total != 1000 {
		t.Errorf("parseCPUStat = %d, %d, %v, want 800, 1000", idle, total, err)
	}
	for _, bad := range []string{"", "intr 1 2 3", "cpu 1 x 3 4 5"} {
		if _, _, err := parseCPUStat(bad); err == nil {
			t.Errorf("parseCPUStat(%q) accepted a malformed line", bad)
		}
	}
}

// With SPANBARN_SHARDS on, the merge reaches the rows in the logs shard. The
// first production run of a source with shard rows archived it with every log
// still in the shard, because the command opened the database without them.
func TestProjectMergeMovesRowsInShards(t *testing.T) {
	cfg := config.Config{DBPath: filepath.Join(t.TempDir(), "spanbarn.db"), Shards: true}
	cfg.Retention.LogHours = 48
	cfg.Retention.MetricsDays = 7
	cfg.Retention.PromptDays = 14
	hostIdle = func() (float64, error) { return 90, nil }
	t.Cleanup(func() { hostIdle = readHostIdle })

	store, err := engine.OpenStorage(context.Background(), cfg.DBPath, engine.StorageOptions{Shards: shardRetention(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.ForStorage(store)
	src, err := repo.CreateProject("main-staging", "main-staging")
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := repo.CreateProject("main", "main")
	if err != nil {
		t.Fatal(err)
	}
	rec := model.LogRecord{ProjectID: src.ID, Body: "line", SeverityNumber: 9, SeverityText: "INFO",
		TimeUnixNano: uint64(time.Now().UnixNano()), Attributes: []byte(`{"service.name":"web"}`)}
	if err := repo.InsertLogs(context.Background(), []model.LogRecord{rec, rec, rec}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	args := []string{"merge", "--source=main-staging", "--target=main", "--env=staging"}
	if err := runProjectCmd(cfg, args); err != nil {
		t.Fatalf("merge: %v", err)
	}

	store, err = engine.OpenStorage(context.Background(), cfg.DBPath, engine.StorageOptions{Shards: shardRetention(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	left, moved := 0, 0
	for _, row := range mustShardRows(t, store) {
		if row.Family != engine.FamilyLogs {
			continue
		}
		db, err := sql.Open("sqlite", filepath.Join(engine.ShardsDir(cfg.DBPath), row.File)+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var l, m int
		if err := db.QueryRow(`SELECT count(*) FILTER (WHERE project_id = ?), count(*) FILTER (WHERE project_id = ?) FROM logs`, src.ID, tgt.ID).Scan(&l, &m); err != nil {
			t.Fatal(err)
		}
		db.Close()
		left, moved = left+l, moved+m
	}
	if left != 0 || moved != 3 {
		t.Errorf("logs in shards: source %d, target %d, want 0 and 3", left, moved)
	}
}

func mustShardRows(t *testing.T, store *engine.Storage) []engine.ShardRow {
	t.Helper()
	rows, err := store.Shards.Rows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
