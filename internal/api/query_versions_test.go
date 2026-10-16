package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

func versionAPISpan(id, status, version string, durationUs int64) repository.Span {
	return repository.Span{
		ProjectID: 1, TraceID: "t" + id, SpanID: "s" + id, Name: "GET /", Service: "web",
		Kind: "server", Status: status, StartTimeUs: time.Now().UnixMicro(), DurationUs: durationUs,
		Attributes: `{"service.version":"` + version + `"}`, Events: `[]`,
	}
}

func TestServiceVersionsEndpoint(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	if err := repo.InsertSpans([]repository.Span{
		versionAPISpan("1", "ok", "1.0.0", 100),
		versionAPISpan("2", "error", "1.0.0", 200),
		versionAPISpan("3", "ok", "1.1.0", 50),
	}); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	now := time.Now().UTC()
	url := "/api/v1/services/web/versions?from=" + now.Add(-time.Hour).Format(time.RFC3339) + "&to=" + now.Add(time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []service.VersionSummary
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 versions, got %d: %+v", len(got), got)
	}
	for _, v := range got {
		if v.Version == "1.0.0" && (v.SpanCount != 2 || v.ErrorRate != 0.5) {
			t.Errorf("1.0.0 = %+v, want 2 spans and error rate 0.5", v)
		}
	}
}

func TestServiceVersionsEndpointNoVersions(t *testing.T) {
	srv, sm, _ := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services/web/versions", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "[]\n" {
		t.Fatalf("a service without versions must return an empty list, got %q", body)
	}
}

func TestServiceVersionsEndpointNoAuth(t *testing.T) {
	srv, _, _ := setupQueryTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services/web/versions", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
