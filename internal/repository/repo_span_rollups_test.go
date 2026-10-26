package repository

import (
	"context"
	"database/sql/driver"
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

func histOf(durations ...int64) []byte {
	var h spanhist.Hist
	for _, d := range durations {
		h.Observe(d)
	}
	return h.Encode()
}

func spanRollupRow(bucket time.Time, name string, durations ...int64) SpanRollup {
	var sum int64
	lo, hi := durations[0], durations[0]
	for _, d := range durations {
		sum += d
		lo, hi = min(lo, d), max(hi, d)
	}
	return SpanRollup{
		StepSeconds: 300, Bucket: bucket, ProjectID: 1, Service: "api", Name: name,
		Kind: "server", IsRoot: true, Status: "ok",
		Count: int64(len(durations)), WeightedCount: 10 * int64(len(durations)), WeightedSpans: int64(len(durations)),
		SumDurationUs: sum, MinUs: lo, MaxUs: hi, Hist: histOf(durations...),
	}
}

func readSpanRollups(t *testing.T, repo *Repository, step int64, from, to time.Time) []SpanRollup {
	t.Helper()
	rows, err := repo.SpanRollupWindow(context.Background(), step, from, to)
	if err != nil {
		t.Fatalf("SpanRollupWindow: %v", err)
	}
	return rows
}

// Rows that differ only in response code are separate rows, and each merges
// on its own key.
func TestAddSpanRollupsKeysOnHTTPStatus(t *testing.T) {
	repo := setupTestDB(t)
	b := time.Date(2026, 10, 26, 12, 5, 0, 0, time.UTC)
	ok, failed := spanRollupRow(b, "GET /", 10), spanRollupRow(b, "GET /", 20)
	ok.HTTPStatus, failed.HTTPStatus = 200, 503
	for range 2 {
		if err := repo.AddSpanRollups([]SpanRollup{ok, failed}); err != nil {
			t.Fatal(err)
		}
	}
	rows := readSpanRollups(t, repo, 300, b, b.Add(5*time.Minute))
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Count != 2 || (r.HTTPStatus != 200 && r.HTTPStatus != 503) {
			t.Errorf("row = %+v", r)
		}
	}
}

// A second flush of the same key adds its counts and merges its histogram.
func TestAddSpanRollupsMergesOnConflict(t *testing.T) {
	repo := setupTestDB(t)
	b := time.Date(2026, 10, 26, 12, 5, 0, 0, time.UTC)
	if err := repo.AddSpanRollups([]SpanRollup{spanRollupRow(b, "GET /", 100, 200)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSpanRollups([]SpanRollup{spanRollupRow(b, "GET /", 50, 9000)}); err != nil {
		t.Fatal(err)
	}
	rows := readSpanRollups(t, repo, 300, b, b.Add(5*time.Minute))
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.Count != 4 || got.WeightedCount != 40 || got.WeightedSpans != 4 || got.SumDurationUs != 9350 ||
		got.MinUs != 50 || got.MaxUs != 9000 || !got.IsRoot || !got.Bucket.Equal(b) {
		t.Errorf("merged row = %+v", got)
	}
	if want := histOf(100, 200, 50, 9000); string(got.Hist) != string(want) {
		t.Errorf("hist = %v, want %v", got.Hist, want)
	}
}

// Replacing a compacted row twice leaves it as written once.
func TestReplaceSpanRollupsIsIdempotent(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	h := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	row := spanRollupRow(h, "GET /", 10, 20, 30)
	row.StepSeconds = 3600
	for range 2 {
		if err := repo.ReplaceSpanRollups(ctx, []SpanRollup{row}); err != nil {
			t.Fatal(err)
		}
	}
	rows := readSpanRollups(t, repo, 3600, h, h.Add(time.Hour))
	if len(rows) != 1 || rows[0].Count != 3 || rows[0].SumDurationUs != 60 {
		t.Errorf("rows = %+v", rows)
	}
}

// A flush larger than one chunk writes every row.
func TestAddSpanRollupsWritesEveryChunk(t *testing.T) {
	repo := setupTestDB(t)
	b := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var rows []SpanRollup
	for i := range spanRollupChunk*2 + 7 {
		rows = append(rows, spanRollupRow(b, fmt.Sprintf("op-%04d", i), int64(i+1)))
	}
	if err := repo.AddSpanRollups(rows); err != nil {
		t.Fatal(err)
	}
	if got := readSpanRollups(t, repo, 300, b, b.Add(5*time.Minute)); len(got) != len(rows) {
		t.Errorf("read %d rows, want %d", len(got), len(rows))
	}
}

func TestSpanRollupWindowAndOldestBucket(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	if _, ok, err := repo.OldestSpanRollupBucket(ctx, 300); err != nil || ok {
		t.Fatalf("empty table: ok=%v err=%v", ok, err)
	}
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var rows []SpanRollup
	for i := range 4 {
		rows = append(rows, spanRollupRow(t0.Add(time.Duration(i)*5*time.Minute), "GET /", 100))
	}
	hourly := spanRollupRow(t0.Add(-time.Hour), "GET /", 100)
	hourly.StepSeconds = 3600
	if err := repo.AddSpanRollups(append(rows, hourly)); err != nil {
		t.Fatal(err)
	}
	oldest, ok, err := repo.OldestSpanRollupBucket(ctx, 300)
	if err != nil || !ok || !oldest.Equal(t0) {
		t.Errorf("oldest 5m bucket = %v ok=%v err=%v, want %v", oldest, ok, err, t0)
	}
	got := readSpanRollups(t, repo, 300, t0.Add(5*time.Minute), t0.Add(15*time.Minute))
	if len(got) != 2 || !got[0].Bucket.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("window = %+v", got)
	}
}

func TestSpanRollupWatermarkOnlyMovesForward(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	if _, ok, err := repo.SpanRollupWatermark(ctx, 300); err != nil || ok {
		t.Fatalf("unset watermark: ok=%v err=%v", ok, err)
	}
	later := time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC)
	for _, through := range []time.Time{later, later.Add(-time.Hour)} {
		if err := repo.SetSpanRollupWatermark(ctx, 300, through); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := repo.SpanRollupWatermark(ctx, 300)
	if err != nil || !ok || !got.Equal(later) {
		t.Errorf("watermark = %v ok=%v err=%v, want %v", got, ok, err, later)
	}
}

// Deletion takes the oldest rows of one tier up to the budget and leaves the
// other tiers alone.
func TestDeleteSpanRollupsOlderThanLimited(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	var rows []SpanRollup
	for i := range 5 {
		rows = append(rows, spanRollupRow(t0.Add(time.Duration(i)*5*time.Minute), "GET /", 100))
	}
	hourly := spanRollupRow(t0, "GET /", 100)
	hourly.StepSeconds = 3600
	if err := repo.AddSpanRollups(append(rows, hourly)); err != nil {
		t.Fatal(err)
	}
	cutoff := t0.Add(20 * time.Minute) // buckets 0..15 are older
	n, more, err := repo.DeleteSpanRollupsOlderThanLimited(ctx, 300, cutoff, 3)
	if err != nil || n != 3 || !more {
		t.Fatalf("first pass: n=%d more=%v err=%v", n, more, err)
	}
	if oldest, _, _ := repo.OldestSpanRollupBucket(ctx, 300); !oldest.Equal(t0.Add(15 * time.Minute)) {
		t.Errorf("after first pass oldest = %v", oldest)
	}
	n, more, err = repo.DeleteSpanRollupsOlderThanLimited(ctx, 300, cutoff, 10)
	if err != nil || n != 1 || more {
		t.Fatalf("second pass: n=%d more=%v err=%v", n, more, err)
	}
	if got := readSpanRollups(t, repo, 3600, t0, t0.Add(time.Hour)); len(got) != 1 {
		t.Errorf("hourly tier lost rows: %+v", got)
	}
}

// A stored histogram that does not decode gives way to the incoming one
// instead of failing the flush.
func TestSpanHistMergeKeepsTheSideThatDecodes(t *testing.T) {
	good := histOf(5, 6)
	for _, tc := range []struct {
		name string
		a, b []byte
		want []byte
	}{
		{"both good", good, histOf(7), histOf(5, 6, 7)},
		{"stored corrupt", []byte{9}, good, good},
		{"incoming corrupt", good, []byte{}, good},
	} {
		got, err := spanHistMerge(nil, []driver.Value{tc.a, tc.b})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if string(got.([]byte)) != string(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMigrateSpansAddsSpanRollupsOnce(t *testing.T) {
	repo := setupCombinedTestDB(t)
	if err := MigrateSpans(context.Background(), repo.db); err != nil {
		t.Fatalf("second spans migration run: %v", err)
	}
	var n int
	if err := repo.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('span_rollups', 'span_rollup_compaction')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("found %d of the 2 span rollup tables", n)
	}
	if TableFamily("span_rollups") != FamilySpans || TableFamily("span_rollup_compaction") != FamilySpans {
		t.Error("span rollup tables must belong to the spans family")
	}
}
