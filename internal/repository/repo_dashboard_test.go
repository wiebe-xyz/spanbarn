package repository

import (
	"fmt"
	"testing"
	"time"
)

// dashSpan inserts one span and pins its ingested_at so bucket assertions do not
// depend on the wall clock. The value is written in Go's time.String() form,
// the shape production stores, because strftime cannot parse it directly.
func dashSpan(t *testing.T, repo *Repository, id, parent, name, service, attrs string, durUs int64, at time.Time) {
	t.Helper()
	s := Span{
		ProjectID: 1, TraceID: "t-" + id, SpanID: id, ParentSpanID: parent,
		Name: name, Service: service, Kind: "server", Status: "ok",
		StartTimeUs: at.UnixMicro(), DurationUs: durUs, Attributes: attrs, Events: "[]",
	}
	if err := repo.InsertSpans([]Span{s}); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if _, err := repo.DB().Exec(`UPDATE spans SET ingested_at = ? WHERE span_id = ?`, at.UTC().String(), id); err != nil {
		t.Fatalf("pin %s: %v", id, err)
	}
}

func TestHTTPStatusGeneratedColumn(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dashSpan(t, repo, "new", "", "a", "web", `{"http.response.status_code": 404}`, 10, at)
	dashSpan(t, repo, "old", "", "a", "web", `{"http.status_code": 200}`, 10, at)
	dashSpan(t, repo, "both", "", "a", "web", `{"http.response.status_code": 500, "http.status_code": 200}`, 10, at)
	dashSpan(t, repo, "none", "", "a", "web", `{}`, 10, at)

	want := map[string]any{"new": int64(404), "old": int64(200), "both": int64(500), "none": nil}
	for id, w := range want {
		var got any
		if err := repo.DB().QueryRow(`SELECT http_status FROM spans WHERE span_id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got != w {
			t.Errorf("%s: http_status = %v, want %v", id, got, w)
		}
	}
}

// A span whose attributes are not valid JSON must still insert: the generated
// column is evaluated on write, and an unguarded json_extract would fail the
// whole batch.
func TestHTTPStatusSurvivesMalformedAttributes(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dashSpan(t, repo, "bad", "", "a", "web", `not json`, 10, at)

	var got any
	if err := repo.DB().QueryRow(`SELECT http_status FROM spans WHERE span_id = 'bad'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("http_status = %v, want NULL", got)
	}
}

func TestQueryDashboardCountsByStatusSkipsMissing(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dashSpan(t, repo, "a", "", "x", "web", `{"http.status_code": 200}`, 10, at)
	dashSpan(t, repo, "b", "", "x", "web", `{"http.status_code": 200}`, 10, at.Add(time.Minute))
	dashSpan(t, repo, "c", "", "x", "web", `{"http.status_code": 500}`, 10, at)
	dashSpan(t, repo, "d", "", "x", "web", `{}`, 10, at)

	pts, err := repo.QueryDashboardCounts(SpanFilter{ProjectID: 1}, 3600, DashboardGroupHTTPStatus, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, p := range pts {
		got[p.Group] += p.Count
	}
	if len(got) != 2 || got["200"] != 2 || got["500"] != 1 {
		t.Errorf("counts by status = %v, want 200:2 500:1", got)
	}
}

func TestQueryDashboardCountsFoldsTail(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i, svc := range []string{"big", "big", "big", "mid", "mid", "s1", "s2"} {
		dashSpan(t, repo, fmt.Sprintf("s%d", i), "", "x", svc, `{}`, 10, at)
	}
	pts, err := repo.QueryDashboardCounts(SpanFilter{ProjectID: 1}, 3600, DashboardGroupService, 2)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, p := range pts {
		got[p.Group] += p.Count
	}
	if got["big"] != 3 || got["mid"] != 2 || got[DashboardOtherGroup] != 2 || len(got) != 3 {
		t.Errorf("folded counts = %v, want big:3 mid:2 other:2", got)
	}
}

func TestQueryDashboardCountsRootOnlyAndProject(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dashSpan(t, repo, "root", "", "x", "web", `{}`, 10, at)
	dashSpan(t, repo, "child", "root", "x", "web", `{}`, 10, at)
	other := Span{ProjectID: 2, TraceID: "o", SpanID: "other", Name: "x", Service: "web", Kind: "server", Status: "ok", Attributes: "{}", Events: "[]"}
	if err := repo.InsertSpans([]Span{other}); err != nil {
		t.Fatal(err)
	}

	pts, err := repo.QueryDashboardCounts(SpanFilter{ProjectID: 1, RootOnly: true}, 3600, DashboardGroupService, 10)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, p := range pts {
		total += p.Count
	}
	if total != 1 {
		t.Errorf("root-only project-1 count = %d, want 1", total)
	}
}

func TestQueryDashboardPercentilesExact(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	// 100 spans, durations 1..100 ms, all in one bucket.
	for i := 1; i <= 100; i++ {
		dashSpan(t, repo, fmt.Sprintf("p%d", i), "", "op", "web", `{}`, int64(i)*1000, at)
	}
	pts, err := repo.QueryDashboardPercentiles(SpanFilter{ProjectID: 1}, 3600, DashboardGroupService, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("points = %d, want 1: %+v", len(pts), pts)
	}
	p := pts[0]
	if p.Count != 100 || p.P90Us != 90_000 || p.P95Us != 95_000 || p.P99Us != 99_000 {
		t.Errorf("percentiles = %+v, want count 100, p90 90000, p95 95000, p99 99000", p)
	}
}

func TestQueryDashboardPercentilesTopNOmitsTail(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i, name := range []string{"hot", "hot", "hot", "warm", "warm", "cold"} {
		dashSpan(t, repo, fmt.Sprintf("n%d", i), "", name, "web", `{}`, 5, at)
	}
	pts, err := repo.QueryDashboardPercentiles(SpanFilter{ProjectID: 1}, 3600, DashboardGroupName, 2)
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]bool{}
	for _, p := range pts {
		groups[p.Group] = true
	}
	if len(groups) != 2 || !groups["hot"] || !groups["warm"] {
		t.Errorf("groups = %v, want hot and warm only", groups)
	}
}

func TestQueryDashboardPercentilesRejectsStatusGroup(t *testing.T) {
	repo := setupTestDB(t)
	if _, err := repo.QueryDashboardPercentiles(SpanFilter{}, 60, DashboardGroupHTTPStatus, 5); err == nil {
		t.Error("want error grouping percentiles by http_status")
	}
}

func TestQueryDashboardHeatmapBuckets(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dashSpan(t, repo, "fast1", "", "x", "web", `{}`, 0, at)
	dashSpan(t, repo, "fast2", "", "x", "web", `{}`, 0, at)
	dashSpan(t, repo, "slow", "", "x", "web", `{}`, 1_000_000, at)

	cells, err := repo.QueryDashboardHeatmap(SpanFilter{ProjectID: 1}, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 {
		t.Fatalf("cells = %+v, want 2", cells)
	}
	if cells[0].DurationBucket != 0 || cells[0].Count != 2 {
		t.Errorf("fast cell = %+v, want bucket 0 count 2", cells[0])
	}
	slow := cells[1]
	lo, hi := HeatmapBucketLowerUs(slow.DurationBucket), HeatmapBucketLowerUs(slow.DurationBucket+1)
	if slow.Count != 1 || lo > 1_000_000 || hi <= 1_000_000 {
		t.Errorf("slow cell = %+v spans [%d,%d), want to contain 1000000", slow, lo, hi)
	}
}

func TestQueryDashboardRejectsUnknownGroup(t *testing.T) {
	repo := setupTestDB(t)
	if _, err := repo.QueryDashboardCounts(SpanFilter{}, 60, DashboardGroup("x; DROP TABLE spans"), 5); err == nil {
		t.Error("want error for unknown group")
	}
}
