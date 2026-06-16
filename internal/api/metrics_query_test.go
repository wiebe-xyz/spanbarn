package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"

	_ "github.com/wiebe-xyz/spanbarn/internal/repository/migrations"
)

// setupMetricsQueryServer creates a server with a real in-memory DB for metrics query tests.
func setupMetricsQueryServer(t *testing.T) (*Server, *auth.SessionManager, *repository.Repository) {
	t.Helper()

	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	repo := repository.NewRepository(db.DB)
	sm := auth.NewSessionManager("test-secret", 3600)

	srv := NewServerWithQuery(ServerConfig{
		APIKey:  "test-key",
		Version: "test",
	}, nil, nil, sm, nil, WithRepository(repo))

	return srv, sm, repo
}

func sessionCookie(t *testing.T, sm *auth.SessionManager) *http.Cookie {
	t.Helper()
	token, err := sm.Create("admin")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: "session", Value: token}
}

func insertTestMetrics(t *testing.T, repo *repository.Repository, recs []model.MetricRecord) {
	t.Helper()
	if err := repo.InsertMetrics(context.Background(), recs); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
}

func TestMetricNamesQuery(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	now := time.Now().UTC()

	insertTestMetrics(t, repo, []model.MetricRecord{
		{ProjectID: 1, Name: "http.server.duration", Type: model.MetricTypeGauge, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
		{ProjectID: 1, Name: "rpc.client.calls", Type: model.MetricTypeSum, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
		{ProjectID: 2, Name: "other.metric", Type: model.MetricTypeGauge, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
	})

	from := now.Add(-1 * time.Hour).Format(time.RFC3339)
	to := now.Add(1 * time.Hour).Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/names?project_id=1&from="+from+"&to="+to, nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp metricNamesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Names) != 2 {
		t.Errorf("expected 2 names for project 1, got %d: %v", len(resp.Names), resp.Names)
	}
	seen := map[string]bool{}
	for _, n := range resp.Names {
		seen[n] = true
	}
	if !seen["http.server.duration"] || !seen["rpc.client.calls"] {
		t.Errorf("unexpected names: %v", resp.Names)
	}
}

func TestMetricNamesQueryAuth(t *testing.T) {
	srv, _, _ := setupMetricsQueryServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/names?project_id=1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestMetricSeriesQuery(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	now := time.Now().UTC()

	insertTestMetrics(t, repo, []model.MetricRecord{
		{
			ProjectID: 1, Name: "http.server.duration", Type: model.MetricTypeGauge,
			Unit: "ms", Value: 42.5, TimeUnixNano: uint64(now.UnixNano()),
			Attributes: []byte(`{"service.name":"web"}`),
		},
		{
			ProjectID: 1, Name: "http.server.duration", Type: model.MetricTypeGauge,
			Unit: "ms", Value: 55.0, TimeUnixNano: uint64(now.Add(time.Second).UnixNano()),
			Attributes: []byte(`{"service.name":"api"}`),
		},
	})

	from := now.Add(-1 * time.Hour).Format(time.RFC3339)
	to := now.Add(1 * time.Hour).Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/metrics/series?project_id=1&name=http.server.duration&from="+from+"&to="+to, nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp metricSeriesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Name != "http.server.duration" {
		t.Errorf("name: want http.server.duration, got %q", resp.Name)
	}
	if resp.Type != "gauge" {
		t.Errorf("type: want gauge, got %q", resp.Type)
	}
	if resp.Unit != "ms" {
		t.Errorf("unit: want ms, got %q", resp.Unit)
	}
	if resp.Render != "line" {
		t.Errorf("render: want line, got %q", resp.Render)
	}
	// Two distinct service.name label sets -> two gauge series, one point each.
	if len(resp.Series) != 2 {
		t.Fatalf("series: want 2, got %d", len(resp.Series))
	}
	for _, s := range resp.Series {
		if len(s.Points) != 1 {
			t.Errorf("series %v: want 1 point, got %d", s.Labels, len(s.Points))
		}
	}
}

func TestMetricSeriesLabelFilter(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	now := time.Now().UTC()

	insertTestMetrics(t, repo, []model.MetricRecord{
		{
			ProjectID: 1, Name: "rpc.calls", Type: model.MetricTypeSum,
			Value: 10, TimeUnixNano: uint64(now.UnixNano()),
			Attributes: []byte(`{"service.name":"svc-a"}`),
		},
		{
			ProjectID: 1, Name: "rpc.calls", Type: model.MetricTypeSum,
			Value: 20, TimeUnixNano: uint64(now.Add(time.Second).UnixNano()),
			Attributes: []byte(`{"service.name":"svc-b"}`),
		},
	})

	from := now.Add(-1 * time.Hour).Format(time.RFC3339)
	to := now.Add(1 * time.Hour).Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/metrics/series?project_id=1&name=rpc.calls&from="+from+"&to="+to+"&label[service.name]=svc-a", nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp metricSeriesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Label filter keeps only svc-a, leaving a single series for that label.
	if len(resp.Series) != 1 {
		t.Fatalf("label filter: want 1 series, got %d", len(resp.Series))
	}
	if resp.Series[0].Labels["service.name"] != "svc-a" {
		t.Errorf("label filter: want service.name=svc-a, got %v", resp.Series[0].Labels)
	}
}

func TestMetricSeriesRollupRouting(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	base := time.Date(2026, 6, 16, 6, 0, 0, 0, time.UTC)

	// Two gauge rollup buckets one hour apart for a single series.
	if err := repo.UpsertMetricRollups([]repository.MetricRollup{
		{ProjectID: 1, Name: "mem.used", Type: "gauge", Unit: "By", AttrFingerprint: "fp", Attributes: `{"host":"h1"}`, Bucket: base, Count: 2, Sum: 200, Min: 90, Max: 110, Last: 110},
		{ProjectID: 1, Name: "mem.used", Type: "gauge", Unit: "By", AttrFingerprint: "fp", Attributes: `{"host":"h1"}`, Bucket: base.Add(time.Hour), Count: 1, Sum: 150, Min: 150, Max: 150, Last: 150},
	}); err != nil {
		t.Fatalf("seed rollups: %v", err)
	}

	// A >6h range routes the query to the rollup table.
	from := base.Add(-time.Hour).Format(time.RFC3339)
	to := base.Add(8 * time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/metrics/series?project_id=1&name=mem.used&from="+from+"&to="+to, nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp metricSeriesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Render != "line" {
		t.Errorf("render: want line, got %q", resp.Render)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("want 1 series from rollups, got %d", len(resp.Series))
	}
	if len(resp.Series[0].Points) != 2 {
		t.Fatalf("want 2 rollup points, got %d", len(resp.Series[0].Points))
	}
	// Gauge value is the bucket average: 200/2 = 100 for the first bucket.
	if resp.Series[0].Points[0].Value != 100 {
		t.Errorf("first bucket avg = %v, want 100", resp.Series[0].Points[0].Value)
	}
}

func TestMetricCatalogGrouping(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	now := time.Now().UTC()

	insertTestMetrics(t, repo, []model.MetricRecord{
		{ProjectID: 1, Name: "http.server.duration", Type: model.MetricTypeHistogram, Unit: "ms", TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{"route":"/a"}`)},
		{ProjectID: 1, Name: "http.server.duration", Type: model.MetricTypeHistogram, Unit: "ms", TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{"route":"/b"}`)},
		{ProjectID: 1, Name: "db.client.calls", Type: model.MetricTypeSum, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
		{ProjectID: 1, Name: "uptime", Type: model.MetricTypeGauge, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
	})

	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/catalog?project_id=1&from="+from+"&to="+to, nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp metricCatalogResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byName := map[string]catalogGroup{}
	for _, g := range resp.Groups {
		byName[g.Name] = g
	}
	if g, ok := byName["http"]; !ok || len(g.Metrics) != 1 || g.Metrics[0].Series != 2 {
		t.Errorf("http group wrong: %+v", byName["http"])
	}
	if _, ok := byName["db"]; !ok {
		t.Errorf("missing db group: %+v", resp.Groups)
	}
	if g, ok := byName["other"]; !ok || g.Metrics[0].Name != "uptime" {
		t.Errorf("dotless metric should fall in 'other': %+v", byName["other"])
	}
}

func TestMetricInsightsSpike(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	base := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)

	// Six hourly gauge buckets: flat ~10 then a jump to ~50 in the recent window.
	vals := []float64{10, 10, 10, 10, 50, 52}
	rollups := make([]repository.MetricRollup, len(vals))
	for i, v := range vals {
		rollups[i] = repository.MetricRollup{
			ProjectID: 1, Name: "cpu.load", Type: "gauge", AttrFingerprint: "fp", Attributes: `{"host":"h1"}`,
			Bucket: base.Add(time.Duration(i) * time.Hour), Count: 1, Sum: v, Min: v, Max: v, Last: v,
		}
	}
	if err := repo.UpsertMetricRollups(rollups); err != nil {
		t.Fatalf("seed: %v", err)
	}

	from := base.Format(time.RFC3339)
	to := base.Add(6 * time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/insights?project_id=1&from="+from+"&to="+to, nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp metricInsightsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Insights) == 0 {
		t.Fatal("expected at least one insight")
	}
	found := false
	for _, in := range resp.Insights {
		if in.Metric == "cpu.load" && in.Kind == "spike" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a cpu.load spike insight, got %+v", resp.Insights)
	}
}

func TestMetricSeriesMissingName(t *testing.T) {
	srv, sm, _ := setupMetricsQueryServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/series?project_id=1", nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestMetricSeriesAuth(t *testing.T) {
	srv, _, _ := setupMetricsQueryServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/series?name=foo&project_id=1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestParseLabelParams(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/test?label[service.name]=web&label[env]=prod&other=ignored", nil)
	labels := parseLabelParams(req)

	if labels["service.name"] != "web" {
		t.Errorf("service.name: want web, got %q", labels["service.name"])
	}
	if labels["env"] != "prod" {
		t.Errorf("env: want prod, got %q", labels["env"])
	}
	if _, ok := labels["other"]; ok {
		t.Error("non-label param should not be included")
	}
}
