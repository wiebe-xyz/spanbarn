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
	if len(resp.Points) != 2 {
		t.Errorf("points: want 2, got %d", len(resp.Points))
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
	if len(resp.Points) != 1 {
		t.Errorf("label filter: want 1 point, got %d", len(resp.Points))
	}
	if len(resp.Points) > 0 && resp.Points[0].Value != 10 {
		t.Errorf("label filter: want value 10, got %v", resp.Points[0].Value)
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
