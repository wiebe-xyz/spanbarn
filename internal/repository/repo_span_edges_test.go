package repository

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

func spanEdgeRow(bucket time.Time, project int64, source, target string, durations ...int64) SpanEdge {
	var sum int64
	lo, hi := durations[0], durations[0]
	for _, d := range durations {
		sum += d
		lo, hi = min(lo, d), max(hi, d)
	}
	return SpanEdge{
		StepSeconds: 300, Bucket: bucket, ProjectID: project, Source: source, Target: target, TargetType: "database",
		Count: int64(len(durations)), ErrorCount: 1, WeightedCount: 4 * int64(len(durations)), WeightedSpans: int64(len(durations)),
		SumDurationUs: sum, MinUs: lo, MaxUs: hi, Hist: histOf(durations...),
	}
}

func scanEdges(t *testing.T, repo *Repository, q SpanEdgeScan) []SpanEdge {
	t.Helper()
	var out []SpanEdge
	if err := repo.ScanSpanEdges(context.Background(), q, func(e SpanEdge) error {
		out = append(out, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// A second flush of the same edge adds its counts and merges its histogram;
// the bucket marker stays one row.
func TestAddSpanEdgesMergesOnConflict(t *testing.T) {
	repo := setupTestDB(t)
	b := time.Date(2026, 10, 26, 12, 5, 0, 0, time.UTC)
	for _, durations := range [][]int64{{100, 200}, {50, 9000}} {
		if err := repo.AddSpanEdges([]SpanEdge{SpanEdgeMarker(300, b), spanEdgeRow(b, 1, "api", "sqlite", durations...)}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := repo.SpanEdgeWindow(context.Background(), 300, b, b.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || !all[0].IsMarker() {
		t.Fatalf("window = %+v, want the marker and one edge", all)
	}
	e := all[1]
	h, err := spanhist.Decode(e.Hist)
	if err != nil {
		t.Fatal(err)
	}
	if e.Count != 4 || e.ErrorCount != 2 || e.WeightedCount != 16 || e.SumDurationUs != 9350 ||
		e.MinUs != 50 || e.MaxUs != 9000 || h.Count() != 4 {
		t.Errorf("merged edge = %+v (hist %d)", e, h.Count())
	}
}

func TestScanSpanEdgesFiltersAndLeavesMarkersOut(t *testing.T) {
	repo := setupTestDB(t)
	b := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := repo.AddSpanEdges([]SpanEdge{
		SpanEdgeMarker(300, b), SpanEdgeMarker(300, b.Add(5*time.Minute)),
		spanEdgeRow(b, 1, "api", "sqlite", 10),
		spanEdgeRow(b, 1, "web", "sqlite", 10),
		spanEdgeRow(b.Add(5*time.Minute), 2, "api", "sqlite", 10),
		spanEdgeRow(b.Add(10*time.Minute), 1, "api", "sqlite", 10),
	}); err != nil {
		t.Fatal(err)
	}
	seg := []SpanRollupSegment{{Step: 300, From: b, To: b.Add(10 * time.Minute)}}
	for _, tc := range []struct {
		name string
		q    SpanEdgeScan
		want int
	}{
		{"every edge", SpanEdgeScan{Segments: seg}, 3},
		{"with markers", SpanEdgeScan{Segments: seg, Markers: true}, 5},
		{"one project", SpanEdgeScan{Segments: seg, ProjectID: 1}, 2},
		{"one source", SpanEdgeScan{Segments: seg, Source: "api"}, 2},
	} {
		if got := scanEdges(t, repo, tc.q); len(got) != tc.want {
			t.Errorf("%s: %d rows, want %d: %+v", tc.name, len(got), tc.want, got)
		}
	}

	ctx := context.Background()
	n, err := repo.CountSpanEdgeBuckets(ctx, 300, b, b.Add(time.Hour))
	if err != nil || n != 3 {
		t.Errorf("edge buckets = %d, %v; want 3", n, err)
	}
	cov, err := repo.SpanEdgeCoverage(ctx)
	if err != nil || !cov.Oldest[300].Equal(b) {
		t.Errorf("coverage = %+v, %v", cov, err)
	}
	if _, ok := cov.Oldest[3600]; ok {
		t.Error("coverage has an hourly tier without hourly rows")
	}
}

func TestDeleteSpanEdgesOlderThanLimited(t *testing.T) {
	repo := setupTestDB(t)
	b := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var rows []SpanEdge
	for i := range 3 {
		at := b.Add(time.Duration(i) * 5 * time.Minute)
		rows = append(rows, SpanEdgeMarker(300, at), spanEdgeRow(at, 1, "api", "sqlite", 10))
	}
	if err := repo.AddSpanEdges(rows); err != nil {
		t.Fatal(err)
	}
	n, more, err := repo.DeleteSpanEdgesOlderThanLimited(context.Background(), 300, b.Add(10*time.Minute), 3)
	if err != nil || n != 3 || !more {
		t.Fatalf("first batch: n=%d more=%v err=%v", n, more, err)
	}
	n, _, err = repo.DeleteSpanEdgesOlderThanLimited(context.Background(), 300, b.Add(10*time.Minute), 10)
	if err != nil || n != 1 {
		t.Fatalf("second batch: n=%d err=%v", n, err)
	}
	left, _ := repo.SpanEdgeWindow(context.Background(), 300, b, b.Add(time.Hour))
	if len(left) != 2 || !left[0].Bucket.Equal(b.Add(10*time.Minute)) {
		t.Errorf("left = %+v, want the last bucket's two rows", left)
	}
}

func TestMigrateSpansAddsSpanEdgesOnce(t *testing.T) {
	repo := setupCombinedTestDB(t)
	if err := MigrateSpans(context.Background(), repo.db); err != nil {
		t.Fatalf("second spans migration run: %v", err)
	}
	var n int
	if err := repo.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'span_edges'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("span_edges is missing")
	}
	if TableFamily("span_edges") != FamilySpans {
		t.Error("span_edges must belong to the spans family")
	}
}
