package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

func heatmapWindow(from, to time.Time, expr *filter.Expr) HeatmapWindow {
	return HeatmapWindow{
		ProjectID: 1, From: from, To: to, Expr: expr,
		MaxSpans: 1000, TimeBuckets: 4, DurationBuckets: 3,
	}
}

func heatSpan(i int, project int64, service string, start time.Time, durationUs int64) Span {
	s := attrSpan(i, "GET /a", service, `{}`)
	s.ProjectID = project
	s.StartTimeUs = start.UnixMicro()
	s.DurationUs = durationUs
	return s
}

func totalCount(cells []HeatmapCell) (n int64) {
	for _, c := range cells {
		n += c.Count
	}
	return n
}

func TestScanHeatmapBucketsSpansByTimeAndDuration(t *testing.T) {
	repo := setupTestDB(t)
	from := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	to := from.Add(2 * time.Hour)
	// Four 30 minute buckets. Fast spans in buckets 0 and 2, slow spans in bucket 3.
	spans := []Span{
		heatSpan(0, 1, "web", from.Add(5*time.Minute), 100),
		heatSpan(1, 1, "web", from.Add(10*time.Minute), 100),
		heatSpan(2, 1, "web", from.Add(65*time.Minute), 100),
		heatSpan(3, 1, "web", from.Add(100*time.Minute), 1_000_000),
		heatSpan(4, 1, "web", from.Add(101*time.Minute), 1_000_000),
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}

	scan, err := repo.ScanHeatmap(context.Background(), heatmapWindow(from, to, nil))
	if err != nil {
		t.Fatal(err)
	}
	if scan.Scanned != 5 || scan.BucketMicros != (30*time.Minute).Microseconds() {
		t.Fatalf("scan = %+v", scan)
	}
	if len(scan.DurationEdgesUs) != 4 || scan.DurationEdgesUs[0] != 100 || scan.DurationEdgesUs[3] != 1_000_000 {
		t.Fatalf("edges = %v", scan.DurationEdgesUs)
	}
	got := map[[2]int]int64{}
	for _, c := range scan.Cells {
		got[[2]int{c.Time, c.Duration}] = c.Count
	}
	want := map[[2]int]int64{{0, 0}: 2, {2, 0}: 1, {3, 2}: 2}
	if len(got) != len(want) {
		t.Fatalf("cells = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("cell %v = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

func TestScanHeatmapEmptyRange(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()
	scan, err := repo.ScanHeatmap(context.Background(), heatmapWindow(now.Add(-time.Hour), now, nil))
	if err != nil {
		t.Fatal(err)
	}
	if scan.Scanned != 0 || len(scan.Cells) != 0 || len(scan.DurationEdgesUs) != 0 {
		t.Fatalf("scan = %+v, want empty", scan)
	}
}

func TestScanHeatmapAppliesFilterAndProject(t *testing.T) {
	repo := setupTestDB(t)
	from := time.Now().UTC().Add(-time.Hour)
	to := from.Add(2 * time.Hour)
	spans := []Span{
		heatSpan(0, 1, "web", from.Add(time.Minute), 100),
		heatSpan(1, 1, "api", from.Add(time.Minute), 200),
		heatSpan(2, 1, "api", from.Add(time.Minute), 300),
		heatSpan(3, 2, "api", from.Add(time.Minute), 400),
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
	expr := &filter.Expr{Match: "and", Filters: []filter.Node{{Key: "service", Op: filter.OpEq, Value: "api"}}}

	scan, err := repo.ScanHeatmap(context.Background(), heatmapWindow(from, to, expr))
	if err != nil {
		t.Fatal(err)
	}
	if scan.Scanned != 2 || totalCount(scan.Cells) != 2 {
		t.Fatalf("filtered scan = %+v, want the 2 project-1 api spans", scan)
	}
	all, err := repo.ScanHeatmap(context.Background(), heatmapWindow(from, to, nil))
	if err != nil {
		t.Fatal(err)
	}
	if all.Scanned != 3 {
		t.Errorf("project 2 leaked: scanned %d, want 3", all.Scanned)
	}
}

func TestScanHeatmapCapsScannedSpans(t *testing.T) {
	repo := setupTestDB(t)
	from := time.Now().UTC().Add(-time.Hour)
	var spans []Span
	for i := 0; i < 10; i++ {
		spans = append(spans, heatSpan(i, 1, "web", from.Add(time.Minute), int64(100+i)))
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
	w := heatmapWindow(from, from.Add(2*time.Hour), nil)
	w.MaxSpans = 4
	scan, err := repo.ScanHeatmap(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Scanned != 4 || totalCount(scan.Cells) != 4 {
		t.Fatalf("scan = %+v, want 4 spans", scan)
	}
}

func TestScanHeatmapSingleDurationAndEarlyStart(t *testing.T) {
	repo := setupTestDB(t)
	from := time.Now().UTC().Add(-time.Hour)
	// Every span has the same duration, one started before the window.
	spans := []Span{
		heatSpan(0, 1, "web", from.Add(-10*time.Minute), 500),
		heatSpan(1, 1, "web", from.Add(time.Minute), 500),
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
	scan, err := repo.ScanHeatmap(context.Background(), heatmapWindow(from, from.Add(2*time.Hour), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Cells) != 1 || scan.Cells[0].Time != 0 || scan.Cells[0].Count != 2 {
		t.Fatalf("cells = %+v, want both spans in time bucket 0", scan.Cells)
	}
	if scan.DurationEdgesUs[3] <= scan.DurationEdgesUs[0] {
		t.Errorf("edges not ascending: %v", scan.DurationEdgesUs)
	}
}

func TestScanHeatmapRejectsBadInput(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()
	w := heatmapWindow(now.Add(-time.Hour), now, nil)
	w.TimeBuckets = 0
	if _, err := repo.ScanHeatmap(context.Background(), w); !errors.Is(err, filter.ErrInvalid) {
		t.Errorf("zero buckets: %v", err)
	}
	w = heatmapWindow(time.Time{}, now, nil)
	if _, err := repo.ScanHeatmap(context.Background(), w); !errors.Is(err, filter.ErrInvalid) {
		t.Errorf("zero from: %v", err)
	}
	bad := &filter.Expr{Filters: []filter.Node{{Key: "duration_us", Op: filter.OpGt, Value: "slow"}}}
	if _, err := repo.ScanHeatmap(context.Background(), heatmapWindow(now.Add(-time.Hour), now, bad)); err == nil {
		t.Error("bad filter accepted")
	}
}

func TestDurationEdges(t *testing.T) {
	e := durationEdges(0, 0, 2)
	if e[0] != 1 || e[2] != 2 {
		t.Errorf("degenerate edges = %v", e)
	}
	e = durationEdges(10, 1000, 2)
	if e[0] != 10 || e[1] != 100 || e[2] != 1000 {
		t.Errorf("log edges = %v, want [10 100 1000]", e)
	}
}
