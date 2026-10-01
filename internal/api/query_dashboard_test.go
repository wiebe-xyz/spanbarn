package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

func dashboardGet(t *testing.T, srv *Server, token, path string, params url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path+"?"+params.Encode(), nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "session", Value: token})
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func dashboardParams(window time.Duration) url.Values {
	now := time.Now().UTC()
	return url.Values{
		"from":       {now.Add(-window).Format(time.RFC3339)},
		"to":         {now.Add(time.Minute).Format(time.RFC3339)},
		"project_id": {"1"},
	}
}

func TestDashboardEndpointsRequireAuth(t *testing.T) {
	srv, _, _ := setupQueryTestServer(t)
	for _, path := range []string{"/api/v1/dashboard/counts", "/api/v1/dashboard/percentiles", "/api/v1/dashboard/heatmap"} {
		if rec := dashboardGet(t, srv, "", path, dashboardParams(time.Hour)); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without session: got %d, want 401", path, rec.Code)
		}
	}
}

func TestDashboardEndpoints(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertSpans([]repository.Span{
		{ProjectID: 1, TraceID: "a", SpanID: "a1", Name: "GET /", Service: "web", Kind: "server", Status: "ok", DurationUs: 4000, Attributes: `{"http.status_code":200}`, Events: "[]"},
		{ProjectID: 1, TraceID: "b", SpanID: "b1", Name: "GET /", Service: "web", Kind: "server", Status: "error", DurationUs: 8000, Attributes: `{"http.status_code":503}`, Events: "[]"},
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("counts by http_status", func(t *testing.T) {
		p := dashboardParams(30 * time.Minute)
		p.Set("group_by", "http_status")
		rec := dashboardGet(t, srv, token, "/api/v1/dashboard/counts", p)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
		var res service.DashboardCounts
		if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
			t.Fatal(err)
		}
		codes := map[string]int64{}
		for _, pt := range res.Points {
			codes[pt.Group] += pt.Count
		}
		if codes["200"] != 1 || codes["503"] != 1 {
			t.Errorf("codes = %v, want 200:1 503:1", codes)
		}
	})

	t.Run("percentiles by name", func(t *testing.T) {
		p := dashboardParams(30 * time.Minute)
		p.Set("group_by", "name")
		rec := dashboardGet(t, srv, token, "/api/v1/dashboard/percentiles", p)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
		var res service.DashboardPercentiles
		if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
			t.Fatal(err)
		}
		if len(res.Points) != 1 || res.Points[0].P99Us != 8000 {
			t.Errorf("points = %+v, want one point with p99 8000", res.Points)
		}
	})

	t.Run("heatmap", func(t *testing.T) {
		rec := dashboardGet(t, srv, token, "/api/v1/dashboard/heatmap", dashboardParams(30*time.Minute))
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
		var res service.DashboardHeatmap
		if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, c := range res.Cells {
			total += c.Count
		}
		if total != 2 {
			t.Errorf("heatmap total = %d, want 2", total)
		}
	})

	t.Run("other project sees nothing", func(t *testing.T) {
		p := dashboardParams(30 * time.Minute)
		p.Set("project_id", "2")
		rec := dashboardGet(t, srv, token, "/api/v1/dashboard/counts", p)
		var res service.DashboardCounts
		if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
			t.Fatal(err)
		}
		if len(res.Points) != 0 {
			t.Errorf("project 2 saw %d points from project 1", len(res.Points))
		}
	})
}

func TestDashboardEndpointsRejectBadRequests(t *testing.T) {
	srv, sm, _ := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}

	wide := dashboardParams(49 * time.Hour)
	badGroup := dashboardParams(time.Hour)
	badGroup.Set("group_by", "bogus")
	cases := map[string]url.Values{
		"range over 48h": wide,
		"unknown group":  badGroup,
		"no range":       {},
	}
	for name, params := range cases {
		if rec := dashboardGet(t, srv, token, "/api/v1/dashboard/counts", params); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}
}
