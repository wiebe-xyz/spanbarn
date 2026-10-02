package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func seedDashboardSpans(t *testing.T, repo *repository.Repository) {
	t.Helper()
	spans := []repository.Span{
		{ProjectID: 1, TraceID: "a", SpanID: "a1", Name: "GET /", Service: "web", Kind: "server", Status: "ok", DurationUs: 2000, Attributes: `{"http.status_code":200}`, Events: "[]"},
		{ProjectID: 1, TraceID: "b", SpanID: "b1", Name: "GET /", Service: "web", Kind: "server", Status: "error", DurationUs: 9000, Attributes: `{"http.response.status_code":500}`, Events: "[]"},
		{ProjectID: 1, TraceID: "b", SpanID: "b2", ParentSpanID: "b1", Name: "SELECT", Service: "db", Kind: "client", Status: "ok", DurationUs: 500, Attributes: "{}", Events: "[]"},
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
}

func dashboardRange() (time.Time, time.Time) {
	now := time.Now().UTC()
	return now.Add(-30 * time.Minute), now.Add(time.Minute)
}

func TestDashboardValidation(t *testing.T) {
	svc := NewQueryService(setupTestRepo(t), nil, nil)
	now := time.Now().UTC()
	cases := map[string]DashboardQuery{
		"missing range": {},
		"inverted":      {From: now, To: now.Add(-time.Hour)},
		"too wide":      {From: now.Add(-49 * time.Hour), To: now},
		"too narrow":    {From: now.Add(-30 * time.Second), To: now},
		"inverted band": {From: now.Add(-time.Hour), To: now, MinDurationUs: 500, MaxDurationUs: 100},
		"negative band": {From: now.Add(-time.Hour), To: now, MinDurationUs: -1},
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.GetDashboardCounts(context.Background(), q, "service", false); !errors.Is(err, ErrInvalidDashboardRequest) {
				t.Errorf("counts err = %v, want ErrInvalidDashboardRequest", err)
			}
			if _, err := svc.GetDashboardPercentiles(context.Background(), q, "service"); !errors.Is(err, ErrInvalidDashboardRequest) {
				t.Errorf("percentiles err = %v, want ErrInvalidDashboardRequest", err)
			}
			if _, err := svc.GetDashboardHeatmap(context.Background(), q, false); !errors.Is(err, ErrInvalidDashboardRequest) {
				t.Errorf("heatmap err = %v, want ErrInvalidDashboardRequest", err)
			}
		})
	}
}

func TestDashboardRejectsUnknownGroup(t *testing.T) {
	svc := NewQueryService(setupTestRepo(t), nil, nil)
	from, to := dashboardRange()
	q := DashboardQuery{From: from, To: to}
	if _, err := svc.GetDashboardCounts(context.Background(), q, "bogus", false); !errors.Is(err, ErrInvalidDashboardRequest) {
		t.Errorf("counts by bogus err = %v, want ErrInvalidDashboardRequest", err)
	}
	if _, err := svc.GetDashboardPercentiles(context.Background(), q, "http_status"); !errors.Is(err, ErrInvalidDashboardRequest) {
		t.Errorf("percentiles by http_status err = %v, want ErrInvalidDashboardRequest", err)
	}
}

func TestDashboardInterval(t *testing.T) {
	cases := []struct {
		window time.Duration
		want   int64
	}{
		{time.Minute, 15}, {15 * time.Minute, 15}, {16 * time.Minute, 60}, {30 * time.Minute, 60}, {time.Hour, 60}, {4 * time.Hour, 300},
		{24 * time.Hour, 900}, {48 * time.Hour, 1800},
	}
	for _, c := range cases {
		if got := dashboardInterval(c.window); got != c.want {
			t.Errorf("dashboardInterval(%s) = %d, want %d", c.window, got, c.want)
		}
	}
}

func TestDashboardCountsAndFilters(t *testing.T) {
	repo := setupTestRepo(t)
	seedDashboardSpans(t, repo)
	svc := NewQueryService(repo, nil, nil)
	from, to := dashboardRange()
	ctx := context.Background()

	byService, err := svc.GetDashboardCounts(ctx, DashboardQuery{ProjectID: 1, From: from, To: to}, "service", false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, p := range byService.Points {
		got[p.Group] += p.Count
	}
	if got["web"] != 2 || got["db"] != 1 {
		t.Errorf("by service = %v, want web:2 db:1", got)
	}

	roots, err := svc.GetDashboardCounts(ctx, DashboardQuery{ProjectID: 1, From: from, To: to}, "service", true)
	if err != nil {
		t.Fatal(err)
	}
	var rootTotal int64
	for _, p := range roots.Points {
		rootTotal += p.Count
	}
	if rootTotal != 2 {
		t.Errorf("root-only total = %d, want 2", rootTotal)
	}

	byStatus, err := svc.GetDashboardCounts(ctx, DashboardQuery{ProjectID: 1, From: from, To: to}, "http_status", false)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]int64{}
	for _, p := range byStatus.Points {
		codes[p.Group] += p.Count
	}
	if len(codes) != 2 || codes["200"] != 1 || codes["500"] != 1 {
		t.Errorf("by status = %v, want 200:1 500:1", codes)
	}

	other, err := svc.GetDashboardCounts(ctx, DashboardQuery{ProjectID: 2, From: from, To: to}, "service", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Points) != 0 {
		t.Errorf("project 2 saw project 1 spans: %+v", other.Points)
	}
}

func TestDashboardPercentilesAndHeatmap(t *testing.T) {
	repo := setupTestRepo(t)
	seedDashboardSpans(t, repo)
	svc := NewQueryService(repo, nil, nil)
	from, to := dashboardRange()
	q := DashboardQuery{ProjectID: 1, From: from, To: to}

	p, err := svc.GetDashboardPercentiles(context.Background(), q, "name")
	if err != nil {
		t.Fatal(err)
	}
	if p.IntervalSeconds != 60 || len(p.Points) == 0 {
		t.Fatalf("percentiles = %+v", p)
	}
	for _, pt := range p.Points {
		if pt.Group == "GET /" && pt.P99Us != 9000 {
			t.Errorf("GET / p99 = %d, want 9000", pt.P99Us)
		}
	}

	h, err := svc.GetDashboardHeatmap(context.Background(), q, false)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, c := range h.Cells {
		total += c.Count
		if c.UpperUs <= c.LowerUs {
			t.Errorf("cell edges [%d,%d) not increasing", c.LowerUs, c.UpperUs)
		}
		if c.LowerUs != repository.HeatmapBucketLowerUs(c.Bucket) {
			t.Errorf("cell bucket %d does not match lower edge %d", c.Bucket, c.LowerUs)
		}
	}
	if total != 3 {
		t.Errorf("heatmap total = %d, want 3", total)
	}
}

func TestDashboardGroupsAndDurationBand(t *testing.T) {
	repo := setupTestRepo(t)
	seedDashboardSpans(t, repo)
	svc := NewQueryService(repo, nil, nil)
	from, to := dashboardRange()
	ctx := context.Background()

	byStatus, err := svc.GetDashboardCounts(ctx, DashboardQuery{ProjectID: 1, From: from, To: to}, "status", false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, p := range byStatus.Points {
		got[p.Group] += p.Count
	}
	if got["ok"] != 2 || got["error"] != 1 {
		t.Errorf("by span status = %v, want ok:2 error:1", got)
	}

	byName, err := svc.GetDashboardCounts(ctx, DashboardQuery{ProjectID: 1, From: from, To: to}, "name", false)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int64{}
	for _, p := range byName.Points {
		names[p.Group] += p.Count
	}
	if names["GET /"] != 2 || names["SELECT"] != 1 {
		t.Errorf("by name = %v, want GET /:2 SELECT:1", names)
	}

	// 1000..5000us keeps only the 2000us span.
	band, err := svc.GetDashboardCounts(ctx, DashboardQuery{ProjectID: 1, From: from, To: to, MinDurationUs: 1000, MaxDurationUs: 5000}, "service", false)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, p := range band.Points {
		total += p.Count
	}
	if total != 1 {
		t.Errorf("duration band count = %d, want 1", total)
	}

	if _, err := svc.GetDashboardPercentiles(ctx, DashboardQuery{ProjectID: 1, From: from, To: to}, "status"); err != nil {
		t.Errorf("percentiles by status: %v", err)
	}
}
