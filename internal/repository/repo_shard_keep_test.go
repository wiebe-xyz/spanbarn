package repository

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// keepFixture writes, into the 4 October logs shard, more pinned rows of
// project 1 than two windows of retentionDeleteBatch hold, more error-sampled
// rows of project 2 than one such window holds, and plain rows of both, all
// with one ingest time.
func keepFixture(t *testing.T) (string, *Storage, *Repository, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T10:00:00Z")
	store := openShardedStorage(t, path, &clock)
	repo := store.Repository()
	ctx := context.Background()
	insert := func(pid int64, trace string, n int) {
		recs := make([]model.LogRecord, n)
		for i := range recs {
			recs[i] = makeLogRecord(pid, trace, "row", 9)
		}
		if err := repo.InsertLogs(ctx, recs); err != nil {
			t.Fatal(err)
		}
	}
	insert(1, "pin", 2*retentionDeleteBatch+500)
	insert(1, "plain", 300)
	insert(2, "err", retentionDeleteBatch+10)
	insert(2, "", 200)
	stampFile(t, filepath.Join(ShardsDir(path), "logs-20261004.db"), "logs", "2026-10-04 10:00:00")
	if err := repo.InsertErrorSamples([]Span{{ProjectID: 2, TraceID: "err", SpanID: "s1", Name: "op", Status: "error", Attributes: "{}", Events: "[]"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PinTrace(ctx, 1, "pin", "kept"); err != nil {
		t.Fatal(err)
	}
	return path, store, repo, &clock
}

func keptCount(t *testing.T, store *Storage, trace string) int {
	t.Helper()
	var n int
	if err := store.Main.QueryRow(`SELECT count(*) FROM kept_logs WHERE trace_id IS ?`, trace).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Retiring the shard copies every protected row, across windows and across
// rows that share one ingest time, and nothing else.
func TestKeepLogsCopiesEveryProtectedRowAcrossWindows(t *testing.T) {
	_, store, repo, clock := keepFixture(t)
	ctx := context.Background()

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
	for trace, want := range map[string]int{"pin": 2*retentionDeleteBatch + 500, "err": retentionDeleteBatch + 10, "plain": 0} {
		if got := keptCount(t, store, trace); got != want {
			t.Errorf("kept %d rows of %s, want %d", got, trace, want)
		}
	}
	if got := mainRows(t, store, "kept_logs"); got != 3*retentionDeleteBatch+510 {
		t.Errorf("kept_logs holds %d rows, want %d", got, 3*retentionDeleteBatch+510)
	}
}

// One write slot copies at most one window: the 2500 pinned rows of project 1
// take ten full windows, and the 300 plain rows after them two more that
// copy nothing.
func TestKeepLogsWindowIsBoundedByRowsScanned(t *testing.T) {
	path, _, repo, _ := keepFixture(t)
	ctx := context.Background()
	shard := filepath.Join(ShardsDir(path), "logs-20261004.db")
	before := mustTime(t, "2026-10-05T00:00:00Z")

	var cur logTrimCursor
	var windows []int64
	for more := true; more; {
		n, m, err := repo.keepLogsWindow(ctx, shard, 1, before, errorLogsSince, &cur)
		if err != nil {
			t.Fatal(err)
		}
		windows = append(windows, n)
		more = m
		if len(windows) > 20 {
			t.Fatalf("no end after %d windows: %v", len(windows), windows)
		}
	}
	// Project 1 has 2800 rows: 2500 pinned, then 300 plain, in rowid order.
	var want []int64
	for range 2500 / keepLogsWindowRows {
		want = append(want, keepLogsWindowRows)
	}
	want = append(want, 0, 0)
	if !slices.Equal(windows, want) {
		t.Fatalf("windows copied %v, want %v", windows, want)
	}
}

// Each keepLogs job reports the rows it copied as write.rows.
func TestKeepLogsJobsReportRowsCopied(t *testing.T) {
	jobRows := recordJobRows(t)
	path, _, repo, _ := keepFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)
	shard := filepath.Join(ShardsDir(path), "logs-20261004.db")

	var cur logTrimCursor
	var copied []int64
	for more := true; more; {
		n, m, err := repo.keepLogsWindow(ctx, shard, 1, mustTime(t, "2026-10-05T00:00:00Z"), errorLogsSince, &cur)
		if err != nil {
			t.Fatal(err)
		}
		copied = append(copied, n)
		more = m
	}
	if got := jobRows()["keepLogs"]; !slices.Equal(got, copied) {
		t.Errorf("keepLogs job rows = %v, want %v", got, copied)
	}
}

// Each window seeks the shard's (project_id, ingested_at) index in order,
// with no sort step, from the start and from a cursor.
func TestKeepLogsSelectPlanSeeksProjectIndex(t *testing.T) {
	path, store, _, _ := keepFixture(t)
	ctx := context.Background()
	conn, err := store.Main.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	shard := filepath.Join(ShardsDir(path), "logs-20261004.db")
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS expiring`, "file:"+shard+"?mode=ro"); err != nil {
		t.Fatal(err)
	}
	for _, resume := range []bool{false, true} {
		rows, err := conn.QueryContext(ctx, "EXPLAIN QUERY PLAN "+keepLogsSelectSQL(resume),
			1, "2030-01-01", "2020-01-01", 1, "2020-01-01", retentionDeleteBatch)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan += detail + "\n"
		}
		rows.Close()
		if !strings.Contains(plan, "idx_logs_project_ingested") {
			t.Errorf("resume=%v: project index not used:\n%s", resume, plan)
		}
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("resume=%v: window sorts:\n%s", resume, plan)
		}
	}
}
