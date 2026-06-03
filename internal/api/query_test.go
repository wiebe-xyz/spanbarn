package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"

	_ "github.com/wiebe-xyz/spanbarn/internal/repository/migrations"
)

func setupQueryTestServer(t *testing.T) (*Server, *auth.SessionManager, *repository.Repository) {
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
	querySvc := service.NewQueryService(repo, nil, nil)
	sm := auth.NewSessionManager("test-secret", 3600)

	srv := NewServerWithQuery(ServerConfig{
		APIKey:  "test-key",
		Version: "test",
	}, nil, querySvc, sm, nil)

	return srv, sm, repo
}

func TestServicesEndpoint(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)

	// Insert test data.
	bucket := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	_ = repo.UpsertAggregate(repository.Aggregate{
		ProjectID: 1, Service: "web", Operation: "GET /",
		Bucket: bucket, Count: 100, ErrorCount: 5,
		P50Us: 500, P95Us: 2000, P99Us: 5000,
	})

	token, err := sm.Create("admin")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services?from=2026-05-03T11:00:00Z&to=2026-05-03T13:00:00Z", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result []service.ServiceSummary
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 service, got %d", len(result))
	}
	if result[0].Service != "web" {
		t.Errorf("expected service 'web', got %q", result[0].Service)
	}
	if result[0].SpanCount != 100 {
		t.Errorf("expected spanCount 100, got %d", result[0].SpanCount)
	}
}

func TestServicesEndpointNoAuth(t *testing.T) {
	srv, _, _ := setupQueryTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTracesEndpoint(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)

	now := time.Now()
	spans := []repository.Span{
		{
			ProjectID: 1, TraceID: "trace-1", SpanID: "s1", Name: "GET /api", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 1000,
			Attributes: "{}", Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "trace-2", SpanID: "s2", Name: "POST /data", Service: "web",
			Kind: "server", Status: "error", StartTimeUs: now.UnixMicro() + 2000, DurationUs: 3000,
			Attributes: "{}", Events: "[]",
		},
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	token, _ := sm.Create("admin")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/traces?service=web&limit=10", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result []service.TraceSummary
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 traces, got %d", len(result))
	}
}

func TestTraceDetailEndpoint(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)

	now := time.Now()
	spans := []repository.Span{
		{
			ProjectID: 1, TraceID: "trace-xyz", SpanID: "root", Name: "GET /", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 5000,
			Attributes: "{}", Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "trace-xyz", SpanID: "child", ParentSpanID: "root", Name: "DB", Service: "web",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 100, DurationUs: 1000,
			Attributes: "{}", Events: "[]",
		},
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	token, _ := sm.Create("admin")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/traces/trace-xyz", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result service.TraceDetail
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.TraceID != "trace-xyz" {
		t.Errorf("expected traceId 'trace-xyz', got %q", result.TraceID)
	}
	if len(result.Spans) != 2 {
		t.Errorf("expected 2 spans, got %d", len(result.Spans))
	}
	if result.DurationUs != 5000 {
		t.Errorf("expected durationUs 5000, got %d", result.DurationUs)
	}
}

func TestTraceDetailEndpointNotFound(t *testing.T) {
	srv, sm, _ := setupQueryTestServer(t)

	token, _ := sm.Create("admin")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/traces/non-existent", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}
