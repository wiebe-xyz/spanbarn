package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"

	_ "github.com/wiebe-xyz/spanbarn/internal/repository/migrations"
)

func setupLogsQueryServer(t *testing.T) (*Server, *SessionService, *repository.Repository) {
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
	sm := NewSessionService(repo, 3600, 3600, nil)

	srv := NewServerWithQuery(ServerConfig{
		APIKey:  "test-key",
		Version: "test",
	}, nil, nil, sm, nil, WithRepository(repo))

	return srv, sm, repo
}

func insertTestLogs(t *testing.T, repo *repository.Repository, recs []model.LogRecord) {
	t.Helper()
	if err := repo.InsertLogs(context.Background(), recs); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}
}

func TestLogsQueryReturnsLogs(t *testing.T) {
	srv, sm, repo := setupLogsQueryServer(t)
	now := time.Now().UTC()

	insertTestLogs(t, repo, []model.LogRecord{
		{ProjectID: 1, Body: "hello", SeverityNumber: 9, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{"service.name":"svc"}`)},
		{ProjectID: 1, Body: "world", SeverityNumber: 13, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{"service.name":"svc"}`)},
		{ProjectID: 2, Body: "other", SeverityNumber: 9, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{"service.name":"svc2"}`)},
	})

	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?project_id=1&from="+from+"&to="+to, nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp logsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("want total=2, got %d", resp.Total)
	}
	if len(resp.Logs) != 2 {
		t.Errorf("want 2 logs, got %d", len(resp.Logs))
	}
}

func TestLogsQueryTraceIDFilter(t *testing.T) {
	srv, sm, repo := setupLogsQueryServer(t)
	now := time.Now().UTC()

	insertTestLogs(t, repo, []model.LogRecord{
		{ProjectID: 1, TraceID: "trace-abc", Body: "msg1", SeverityNumber: 9, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
		{ProjectID: 1, TraceID: "trace-def", Body: "msg2", SeverityNumber: 9, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
	})

	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?project_id=1&trace_id=trace-abc&from="+from+"&to="+to, nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp logsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("want total=1, got %d", resp.Total)
	}
	if len(resp.Logs) == 0 || resp.Logs[0].Body != "msg1" {
		t.Errorf("unexpected logs: %+v", resp.Logs)
	}
}

func TestLogsQuerySeverityFilter(t *testing.T) {
	srv, sm, repo := setupLogsQueryServer(t)
	now := time.Now().UTC()

	insertTestLogs(t, repo, []model.LogRecord{
		{ProjectID: 1, Body: "info", SeverityNumber: 9, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
		{ProjectID: 1, Body: "warn", SeverityNumber: 13, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
		{ProjectID: 1, Body: "error", SeverityNumber: 17, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
	})

	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?project_id=1&severity=13&from="+from+"&to="+to, nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp logsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("want total=2 (warn+error), got %d", resp.Total)
	}
}

func TestLogsQueryBodySearch(t *testing.T) {
	srv, sm, repo := setupLogsQueryServer(t)
	now := time.Now().UTC()

	insertTestLogs(t, repo, []model.LogRecord{
		{ProjectID: 1, Body: "connection timeout", SeverityNumber: 17, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
		{ProjectID: 1, Body: "request OK", SeverityNumber: 9, TimeUnixNano: uint64(now.UnixNano()), Attributes: []byte(`{}`)},
	})

	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?project_id=1&search=connection&from="+from+"&to="+to, nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp logsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("want total=1, got %d", resp.Total)
	}
}

func TestPinUnpinRoundTrip(t *testing.T) {
	srv, sm, _ := setupLogsQueryServer(t)

	cookie := sessionCookie(t, sm)

	// Pin a trace.
	body := bytes.NewBufferString(`{"project_id":1,"trace_id":"trace-abc","label":"checkout bug"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pinned-traces", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("pin: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// List pinned traces.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/pinned-traces?project_id=1", nil)
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rec2.Code)
	}
	var listResp pinnedTracesResponse
	if err := json.NewDecoder(rec2.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listResp.Pinned) != 1 || listResp.Pinned[0].TraceID != "trace-abc" {
		t.Errorf("unexpected list: %+v", listResp)
	}

	// Unpin by path.
	req3 := httptest.NewRequest(http.MethodDelete, "/api/v1/pinned-traces/trace-abc?project_id=1", nil)
	req3.AddCookie(cookie)
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusNoContent {
		t.Fatalf("unpin: expected 204, got %d", rec3.Code)
	}

	// Confirm empty list.
	req4 := httptest.NewRequest(http.MethodGet, "/api/v1/pinned-traces?project_id=1", nil)
	req4.AddCookie(cookie)
	rec4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec4, req4)

	var listResp2 pinnedTracesResponse
	if err := json.NewDecoder(rec4.Body).Decode(&listResp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listResp2.Pinned) != 0 {
		t.Errorf("expected empty list after unpin, got %d", len(listResp2.Pinned))
	}
}

func TestLogsQueryAuthRequired(t *testing.T) {
	srv, _, _ := setupLogsQueryServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?project_id=1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rec.Code)
	}
}

func TestPinnedTracesAuthRequired(t *testing.T) {
	srv, _, _ := setupLogsQueryServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pinned-traces?project_id=1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rec.Code)
	}
}
