package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestDashboardGrainGrowsWithTheWindow(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		time.Minute:      dashboardFineGrain,
		time.Hour:        dashboardFineGrain,
		4 * time.Hour:    dashboardFineGrain,
		12 * time.Hour:   dashboardCoarseGrain,
		24 * time.Hour:   dashboardCoarseGrain,
		48 * time.Hour:   dashboardCoarseGrain,
		11 * time.Hour:   dashboardFineGrain,
		12*time.Hour - 1: dashboardFineGrain,
	}
	for window, want := range cases {
		if got := dashboardGrain(window); got != want {
			t.Errorf("dashboardGrain(%s) = %s, want %s", window, got, want)
		}
	}
}

func TestSnappedKeepsTheWindowWidth(t *testing.T) {
	to := time.Date(2026, 10, 15, 9, 37, 41, 0, time.UTC)
	q := DashboardQuery{From: to.Add(-24 * time.Hour), To: to}.snapped()
	if got := q.To.Sub(q.From); got != 24*time.Hour {
		t.Errorf("snapped width = %s, want 24h", got)
	}
	if want := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC); !q.To.Equal(want) {
		t.Errorf("snapped To = %s, want %s", q.To, want)
	}
}

func TestWarmDashboardFillsTheLandingViewCache(t *testing.T) {
	repo := setupTestRepo(t)
	seedDashboardSpans(t, repo)
	svc := NewQueryService(repo, nil, nil)
	svc.SetCache(cache.New(cache.NewMemoryStore(), time.Minute))

	// A period start after the seeded spans, so they sit inside the window.
	base := time.Now().UTC().Add(16 * time.Minute).Truncate(dashboardCoarseGrain)
	if err := svc.WarmDashboard(context.Background(), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if err := repo.InsertSpans([]repository.Span{
		{ProjectID: 1, TraceID: "c", SpanID: "c1", Name: "GET /", Service: "web", Kind: "server", Status: "ok", DurationUs: 100, Attributes: "{}", Events: "[]"},
	}); err != nil {
		t.Fatal(err)
	}

	// A viewer ten minutes into the same period asks for the page's default view.
	viewer := base.Add(10 * time.Minute)
	q := DashboardQuery{From: viewer.Add(-24 * time.Hour), To: viewer}
	counts, err := svc.GetDashboardCounts(context.Background(), q, "service", true)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, p := range counts.Points {
		total += p.Count
	}
	if total != 2 {
		t.Errorf("root span total = %d, want the warmed 2 (the span inserted after the warm must not show)", total)
	}

	// The other cards of the page answer from the same snapped window.
	for name, call := range map[string]func() error{
		"counts by status": func() error { _, e := svc.GetDashboardCounts(context.Background(), q, "http_status", true); return e },
		"heatmap root":     func() error { _, e := svc.GetDashboardHeatmap(context.Background(), q, true); return e },
		"heatmap all":      func() error { _, e := svc.GetDashboardHeatmap(context.Background(), q, false); return e },
		"percentiles svc":  func() error { _, e := svc.GetDashboardPercentiles(context.Background(), q, "service"); return e },
		"percentiles name": func() error { _, e := svc.GetDashboardPercentiles(context.Background(), q, "name"); return e },
	} {
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The warmer scans on its own budget. On the 200m reader pod the 24h landing
// view outlasts the 30s per-query timeout at busy quarter hours, and every warm
// that did failed (SPA-78, SPA-81), because the cache detached the scan from
// the warmer's 5-minute deadline. The per-query timeout here has already
// expired, so a scan that falls back to it fails at once.
func TestWarmDashboardScansOnTheWarmersBudget(t *testing.T) {
	repo := setupTestRepo(t)
	seedDashboardSpans(t, repo)
	repo.SetQueryTimeout(-time.Second)
	svc := NewQueryService(repo, nil, nil)
	svc.SetCache(cache.New(cache.NewMemoryStore(), time.Minute))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	now := time.Now().UTC()
	if err := svc.WarmDashboard(ctx, now); err != nil {
		t.Fatalf("warm on a 1m budget: %v", err)
	}

	// A window nobody warmed, asked for without a deadline, keeps the cap.
	q := DashboardQuery{From: now.Add(-time.Hour), To: now}
	if _, err := svc.GetDashboardCounts(context.Background(), q, "service", true); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("request without a deadline: err = %v, want the per-query timeout", err)
	}
}
