package repository

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

var readBase = time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

func addRollups(t *testing.T, repo *Repository, rows ...SpanRollup) {
	t.Helper()
	if err := repo.AddSpanRollups(rows); err != nil {
		t.Fatal(err)
	}
}

func fiveMinutes(from, to time.Time) []SpanRollupSegment {
	return []SpanRollupSegment{{Step: 300, From: from, To: to}}
}

func TestSpanRollupCoverage(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	cov, err := repo.SpanRollupCoverage(ctx)
	if err != nil || len(cov.Oldest) != 0 || len(cov.Through) != 0 {
		t.Fatalf("empty coverage = %+v, %v", cov, err)
	}
	hour := spanRollupRow(readBase, "GET /", 10)
	hour.StepSeconds = 3600
	addRollups(t, repo, spanRollupRow(readBase.Add(10*time.Minute), "GET /", 10), spanRollupRow(readBase.Add(time.Hour), "GET /", 10), hour)
	if err := repo.SetSpanRollupWatermark(ctx, 300, readBase.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cov, err = repo.SpanRollupCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cov.Oldest[300].Equal(readBase.Add(10*time.Minute)) || !cov.Oldest[3600].Equal(readBase) {
		t.Errorf("oldest = %v", cov.Oldest)
	}
	if _, ok := cov.Oldest[86400]; ok {
		t.Errorf("day tier is empty, oldest = %v", cov.Oldest)
	}
	if !cov.Through[300].Equal(readBase.Add(time.Hour)) || len(cov.Through) != 1 {
		t.Errorf("through = %v", cov.Through)
	}
}

func TestScanSpanRollupsFilters(t *testing.T) {
	repo := setupTestDB(t)
	child := spanRollupRow(readBase, "SELECT", 5)
	child.IsRoot, child.Kind = false, "client"
	otherProject := spanRollupRow(readBase, "GET /", 5)
	otherProject.ProjectID = 2
	failed := spanRollupRow(readBase, "GET /", 5)
	failed.Status = "error"
	worker := spanRollupRow(readBase, "GET /", 5)
	worker.Service = "worker"
	addRollups(t, repo,
		spanRollupRow(readBase, "GET /", 5), spanRollupRow(readBase, "GET /health", 5),
		spanRollupRow(readBase.Add(time.Hour), "GET /", 5), // past the segment
		child, otherProject, failed, worker)

	count := func(q SpanRollupScan) int {
		t.Helper()
		q.Segments = fiveMinutes(readBase, readBase.Add(time.Hour))
		n := 0
		if err := repo.ScanSpanRollups(context.Background(), q, func(SpanRollup) error { n++; return nil }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for name, tc := range map[string]struct {
		q    SpanRollupScan
		want int
	}{
		"every row in the segment": {SpanRollupScan{}, 6},
		"project":                  {SpanRollupScan{ProjectID: 1}, 5},
		"service":                  {SpanRollupScan{Service: "worker"}, 1},
		"name":                     {SpanRollupScan{Name: "GET /health"}, 1},
		"status":                   {SpanRollupScan{Status: "error"}, 1},
		"root only":                {SpanRollupScan{RootOnly: true}, 5},
		"kind":                     {SpanRollupScan{Kind: "client"}, 1},
		"excluded names":           {SpanRollupScan{ExcludeNames: []string{"GET /health", "SELECT"}}, 4},
	} {
		if got := count(tc.q); got != tc.want {
			t.Errorf("%s: %d rows, want %d", name, got, tc.want)
		}
	}
}

// The bucket count sees every project and only buckets that hold rows.
func TestCountSpanRollupBuckets(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	otherProject := spanRollupRow(readBase.Add(10*time.Minute), "GET /", 5)
	otherProject.ProjectID = 2
	hour := spanRollupRow(readBase, "GET /", 5)
	hour.StepSeconds = 3600
	addRollups(t, repo,
		spanRollupRow(readBase, "GET /", 5), spanRollupRow(readBase, "GET /health", 5), // two rows, one bucket
		otherProject, // 10:05 holds nothing
		spanRollupRow(readBase.Add(time.Hour), "GET /", 5), hour)
	for _, tc := range []struct {
		step     int64
		from, to time.Time
		want     int
	}{
		{300, readBase, readBase.Add(time.Hour), 2},
		{300, readBase, readBase.Add(time.Hour + time.Second), 3},
		{300, readBase.Add(time.Minute), readBase.Add(10 * time.Minute), 0},
		{3600, readBase, readBase.Add(24 * time.Hour), 1},
	} {
		got, err := repo.CountSpanRollupBuckets(ctx, tc.step, tc.from, tc.to)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("step %d [%v, %v): %d buckets, want %d", tc.step, tc.from, tc.to, got, tc.want)
		}
	}
}

// Counts are received spans: a weighted row counts its weights. The SQL sum
// and the banded scan over every bin agree.
func TestQueryDashboardCountsFromRollups(t *testing.T) {
	repo := setupTestDB(t)
	weighted := spanRollupRow(readBase, "GET /", 10, 20) // 2 spans of weight 10
	plain := spanRollupRow(readBase.Add(5*time.Minute), "GET /", 10)
	plain.WeightedCount, plain.WeightedSpans = 0, 0
	plain.HTTPStatus = 503
	worker := spanRollupRow(readBase.Add(20*time.Minute), "poll", 10, 10, 10)
	worker.Service, worker.WeightedCount, worker.WeightedSpans = "worker", 0, 0
	addRollups(t, repo, weighted, plain, worker)

	q := DashboardRollupQuery{Scan: SpanRollupScan{Segments: fiveMinutes(readBase, readBase.Add(time.Hour))}, Interval: 900}
	for _, banded := range []bool{false, true} {
		if banded {
			q.BinHi = spanhist.Bins
		}
		pts, err := repo.QueryDashboardCountsFromRollups(context.Background(), q, DashboardGroupService, 10)
		if err != nil {
			t.Fatal(err)
		}
		want := []DashboardCountPoint{
			{Bucket: readBase, Group: "api", Count: 21},
			{Bucket: readBase.Add(15 * time.Minute), Group: "worker", Count: 3},
		}
		if len(pts) != len(want) {
			t.Fatalf("banded=%v: points = %+v", banded, pts)
		}
		for i := range want {
			if pts[i] != want[i] {
				t.Errorf("banded=%v: point %d = %+v, want %+v", banded, i, pts[i], want[i])
			}
		}
	}

	q.BinHi = 0
	pts, err := repo.QueryDashboardCountsFromRollups(context.Background(), q, DashboardGroupHTTPStatus, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 || pts[0].Group != "503" || pts[0].Count != 1 {
		t.Errorf("http status points = %+v, want one 503", pts)
	}
}

// A band keeps the bins inside it and scales the received count by the share
// of the row's spans it kept.
func TestQueryDashboardCountsFromRollupsBand(t *testing.T) {
	repo := setupTestDB(t)
	addRollups(t, repo, spanRollupRow(readBase, "GET /", 10, 10, 5000, 5000)) // 4 spans of weight 10
	lo := 2 * heatmapIndex(5000)
	q := DashboardRollupQuery{Scan: SpanRollupScan{Segments: fiveMinutes(readBase, readBase.Add(time.Hour))}, Interval: 300, BinLo: lo}
	pts, err := repo.QueryDashboardCountsFromRollups(context.Background(), q, DashboardGroupName, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 || pts[0].Count != 20 {
		t.Errorf("points = %+v, want 20 received spans of 5000us", pts)
	}
}

// heatmapIndex is the half-octave bucket a duration falls in.
func heatmapIndex(us int64) int { return spanhist.Index(us) >> 1 }

func TestQueryDashboardPercentilesFromRollups(t *testing.T) {
	repo := setupTestDB(t)
	slow := spanRollupRow(readBase.Add(5*time.Minute), "GET /slow", 4000, 8000)
	addRollups(t, repo, spanRollupRow(readBase, "GET /", 100, 200), spanRollupRow(readBase.Add(5*time.Minute), "GET /", 300), slow)
	q := DashboardRollupQuery{Scan: SpanRollupScan{Segments: fiveMinutes(readBase, readBase.Add(time.Hour))}, Interval: 900}
	pts, err := repo.QueryDashboardPercentilesFromRollups(context.Background(), q, DashboardGroupName, 1)
	if err != nil {
		t.Fatal(err)
	}
	var want spanhist.Hist
	for _, d := range []int64{100, 200, 300} {
		want.Observe(d)
	}
	if len(pts) != 1 || pts[0].Group != "GET /" || pts[0].Count != 30 || pts[0].P99Us != want.Percentile(99) {
		t.Errorf("points = %+v, want the top group only with p99 %d", pts, want.Percentile(99))
	}
	if _, err := repo.QueryDashboardPercentilesFromRollups(context.Background(), q, DashboardGroupHTTPStatus, 1); err == nil {
		t.Error("grouping percentiles by HTTP status succeeded")
	}
}

func TestQueryDashboardHeatmapFromRollups(t *testing.T) {
	repo := setupTestDB(t)
	addRollups(t, repo, spanRollupRow(readBase, "GET /", 10, 10, 5000))
	q := DashboardRollupQuery{Scan: SpanRollupScan{Segments: fiveMinutes(readBase, readBase.Add(time.Hour))}, Interval: 300}
	cells, err := repo.QueryDashboardHeatmapFromRollups(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	want := []DashboardHeatmapCell{
		{Bucket: readBase, DurationBucket: heatmapIndex(10), Count: 20},
		{Bucket: readBase, DurationBucket: heatmapIndex(5000), Count: 10},
	}
	if len(cells) != len(want) || cells[0] != want[0] || cells[1] != want[1] {
		t.Errorf("cells = %+v, want %+v", cells, want)
	}
}

// Root groups add counts, errors and weights across rows and tiers, and leave
// child rows out.
func TestQueryRootSpanGroupsFromRollups(t *testing.T) {
	repo := setupTestDB(t)
	failed := spanRollupRow(readBase.Add(5*time.Minute), "GET /", 900)
	failed.Status = "error"
	hour := spanRollupRow(readBase.Add(time.Hour), "GET /", 50)
	hour.StepSeconds = 3600
	child := spanRollupRow(readBase, "GET /", 1)
	child.IsRoot = false
	addRollups(t, repo, spanRollupRow(readBase, "GET /", 100, 200), failed, hour, child)

	groups, err := repo.QueryRootSpanGroupsFromRollups(context.Background(), SpanRollupScan{Segments: []SpanRollupSegment{
		{Step: 300, From: readBase, To: readBase.Add(time.Hour)},
		{Step: 3600, From: readBase.Add(time.Hour), To: readBase.Add(2 * time.Hour)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	g := groups[0]
	if g.Operation != "GET /" || g.Service != "api" || g.Count != 4 || g.ErrorCount != 1 ||
		g.Weights.Count != 40 || g.Weights.Spans != 4 || g.Hist.Count() != 4 {
		t.Errorf("group = %+v", g)
	}
}

// SpansIngestedBetween is half-open on whole seconds and sees every project.
func TestSpansIngestedBetween(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	dashSpan(t, repo, "a", "", "GET /", "api", "{}", 10, readBase.Add(5*time.Minute))
	dashSpan(t, repo, "b", "", "GET /", "api", "{}", 10, readBase.Add(15*time.Minute-time.Second))
	for _, tc := range []struct {
		from, to time.Time
		want     bool
	}{
		{readBase, readBase.Add(5 * time.Minute), false},
		{readBase.Add(5 * time.Minute), readBase.Add(10 * time.Minute), true},
		{readBase.Add(10 * time.Minute), readBase.Add(15 * time.Minute), true},
		{readBase.Add(15 * time.Minute), readBase.Add(time.Hour), false},
	} {
		got, err := repo.SpansIngestedBetween(ctx, tc.from, tc.to)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("[%v, %v) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}
