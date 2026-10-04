package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// shardedReadFixture writes one batch of logs, metrics and prompts to main
// before shards were on, one at 23:59:30 on 4 October and one at 00:00:30 on
// 5 October, so every family has rows in main and in two shards. It returns
// the writer storage, a read repository with shard readers, and the clock.
func shardedReadFixture(t *testing.T) (string, *Storage, *Repository, *ShardReaders, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spanbarn.db")

	plain := openTestStorage(t, path)
	insertReadBatch(t, plain.Repository(), "main")
	stampFile(t, path, "logs", "2026-10-04 12:00:00")
	stampFile(t, path, "prompt_records", "2026-10-04 12:00:00")
	plain.Close()

	clock := mustTime(t, "2026-10-04T23:59:30Z")
	store := openShardedStorage(t, path, &clock)
	repo := store.Repository()
	insertReadBatch(t, repo, "day1")
	stampFile(t, filepath.Join(ShardsDir(path), "logs-20261004.db"), "logs", "2026-10-04 23:59:30")
	stampFile(t, filepath.Join(ShardsDir(path), "prompts-2026w40.db"), "prompt_records", "2026-10-04 23:59:30")
	clock = mustTime(t, "2026-10-05T00:00:30Z")
	insertReadBatch(t, repo, "day2")
	stampFile(t, filepath.Join(ShardsDir(path), "logs-20261005.db"), "logs", "2026-10-05 00:00:30")
	stampFile(t, filepath.Join(ShardsDir(path), "prompts-2026w41.db"), "prompt_records", "2026-10-05 00:00:30")

	ro, err := OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	readRepo := NewReadOnlyRepository(ro.DB)
	rs := NewShardReaders(ro.DB, path, 2, 0, nil)
	t.Cleanup(rs.Close)
	readRepo.SetShardReaders(rs)
	if err := rs.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return path, store, readRepo, rs, &clock
}

func insertReadBatch(t *testing.T, repo *Repository, tag string) {
	t.Helper()
	ctx := context.Background()
	if err := repo.InsertLogs(ctx, []model.LogRecord{makeLogRecord(1, "trace-x", "log "+tag, 9)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertMetrics(ctx, []model.MetricRecord{makeMetricRecord(1, "cpu."+tag, model.MetricTypeGauge, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertPromptRecords([]PromptRecord{{ProjectID: 1, TraceID: "trace-x", SpanID: "s-" + tag, Name: "chat " + tag, Model: "m"}}); err != nil {
		t.Fatal(err)
	}
}

// stampFile sets ingested_at of every row of table in the file at path.
func stampFile(t *testing.T, path, table, at string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE `+table+` SET ingested_at = ?`, at); err != nil {
		t.Fatalf("stamp %s in %s: %v", table, path, err)
	}
}

// logsFrom and logsTo span every ingest time the fixture stamps; the logs
// queries take a required window.
var (
	logsFrom = time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	logsTo   = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
)

func TestShardReadsLogsAcrossMidnight(t *testing.T) {
	_, _, repo, _, _ := shardedReadFixture(t)
	ctx := context.Background()

	rows, total, err := repo.QueryLogs(ctx, LogFilter{ProjectID: 1, TraceID: "trace-x", From: logsFrom, To: logsTo, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("logs of trace-x: total %d, rows %d, want 3 and 3", total, len(rows))
	}
	var bodies []string
	ids := map[int64]bool{}
	for _, r := range rows {
		bodies = append(bodies, r.Body)
		ids[r.ID] = true
	}
	slices.Sort(bodies)
	if want := []string{"log day1", "log day2", "log main"}; !slices.Equal(bodies, want) {
		t.Fatalf("bodies %v, want %v", bodies, want)
	}
	if len(ids) != 3 {
		t.Fatalf("log ids collide across shards: %v", rows)
	}

	buckets, err := repo.LogHistogram(ctx, LogFilter{
		ProjectID: 1,
		From:      logsFrom,
		To:        logsTo,
	}, 3600)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range buckets {
		n += b.Count
	}
	if n != 3 {
		t.Fatalf("histogram counts %d logs, want 3: %v", n, buckets)
	}
}

func TestShardReadsMetrics(t *testing.T) {
	_, _, repo, _, _ := shardedReadFixture(t)
	ctx := context.Background()

	names, err := repo.ListMetricNames(ctx, 1, time.Time{}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if want := []string{"cpu.day1", "cpu.day2", "cpu.main"}; !slices.Equal(names, want) {
		t.Fatalf("metric names %v, want %v", names, want)
	}
	catalog, err := repo.ListMetricCatalog(ctx, 1, time.Time{}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 3 {
		t.Fatalf("catalog has %d entries, want 3", len(catalog))
	}
	for _, name := range names {
		series, err := repo.QueryMetricSeries(ctx, MetricFilter{ProjectID: 1, Name: name, To: time.Now().Add(time.Hour), Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(series) != 1 {
			t.Fatalf("series %s has %d points, want 1", name, len(series))
		}
	}
}

func TestShardReadsPromptsNewestFirst(t *testing.T) {
	_, _, repo, _, _ := shardedReadFixture(t)

	names := func(recs []PromptRecord) []string {
		out := make([]string, len(recs))
		for i, r := range recs {
			out[i] = r.Name
		}
		return out
	}
	all, err := repo.QueryPromptRecords(PromptFilter{ProjectID: 1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(all), []string{"chat day2", "chat day1", "chat main"}; !slices.Equal(got, want) {
		t.Fatalf("prompts %v, want %v", got, want)
	}
	page, err := repo.QueryPromptRecords(PromptFilter{ProjectID: 1, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(page); !slices.Equal(got, []string{"chat day1"}) {
		t.Fatalf("second page %v, want [chat day1]", got)
	}
	past, err := repo.QueryPromptRecords(PromptFilter{ProjectID: 1, Limit: 5, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(past) != 0 {
		t.Fatalf("page past the end has %d rows", len(past))
	}
	since, err := repo.QueryPromptRecords(PromptFilter{ProjectID: 1, From: mustTime(t, "2026-10-04T23:00:00Z"), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(since); !slices.Equal(got, []string{"chat day2", "chat day1"}) {
		t.Fatalf("prompts since 23:00 %v", got)
	}

	byTrace, err := repo.GetPromptRecordsByTraceID("trace-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(byTrace) != 3 {
		t.Fatalf("prompts of trace-x: %d, want 3", len(byTrace))
	}
	ids := map[int64]bool{}
	for _, r := range byTrace {
		ids[r.ID] = true
	}
	if len(ids) != 3 {
		t.Fatalf("prompt ids collide across shards: %v", names(byTrace))
	}
}

func TestShardReadersPickUpNewShard(t *testing.T) {
	path, store, repo, rs, clock := shardedReadFixture(t)
	ctx := context.Background()

	*clock = mustTime(t, "2026-10-06T00:00:30Z")
	if err := store.Shards.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Repository().InsertLogs(ctx, []model.LogRecord{makeLogRecord(1, "trace-x", "log day3", 9)}); err != nil {
		t.Fatal(err)
	}
	stampFile(t, filepath.Join(ShardsDir(path), "logs-20261006.db"), "logs", "2026-10-06 00:00:30")
	count := func() int {
		_, total, err := repo.QueryLogs(ctx, LogFilter{ProjectID: 1, TraceID: "trace-x", From: logsFrom, To: logsTo, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return total
	}
	if n := count(); n != 3 {
		t.Fatalf("before refresh: %d logs, want 3", n)
	}
	if err := rs.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 4 {
		t.Fatalf("after refresh: %d logs, want 4", n)
	}
}

func TestShardReadersWithoutShards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store := openTestStorage(t, path)
	insertReadBatch(t, store.Repository(), "main")

	ro, err := OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	repo := NewReadOnlyRepository(ro.DB)
	rs := NewShardReaders(ro.DB, path, 2, 0, nil)
	defer rs.Close()
	repo.SetShardReaders(rs)
	if err := rs.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := rs.pool(FamilyLogs); p != nil {
		t.Fatal("a database without shards has a logs pool")
	}
	recs, err := repo.QueryPromptRecords(PromptFilter{ProjectID: 1, Limit: 10})
	if err != nil || len(recs) != 1 {
		t.Fatalf("prompts without shards: %d, %v", len(recs), err)
	}
}

func TestShardIDsStartAtPeriodBase(t *testing.T) {
	path, _, _, _, _ := shardedReadFixture(t)
	db, err := sql.Open("sqlite", filepath.Join(ShardsDir(path), "logs-20261005.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id int64
	if err := db.QueryRow(`SELECT id FROM logs`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	base := shardIDBase(mustTime(t, "2026-10-05T00:00:00Z"))
	if id != base+1 {
		t.Fatalf("first id of the 5 October shard is %d, want %d", id, base+1)
	}
	if id >= 1<<53 {
		t.Fatalf("id %d does not fit a JSON number", id)
	}
}
