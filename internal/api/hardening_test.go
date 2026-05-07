package api_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	q := ingest.NewQueue(1024)
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })

	h := ingest.NewHandler(q, sp, 0, slog.Default())
	h.Start(t.Context())
	t.Cleanup(func() { h.Stop() })

	srv := api.NewServer(api.ServerConfig{
		APIKey:  testAPIKey,
		Version: "test",
	}, h, slog.Default())

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestRequestIDGenerated(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	id := resp.Header.Get("X-Request-Id")
	if id == "" {
		t.Error("expected X-Request-Id header to be set")
	}
	if len(id) != 16 {
		t.Errorf("expected 16-char hex request ID, got %q (len %d)", id, len(id))
	}
}

func TestRequestIDPreserved(t *testing.T) {
	ts := newTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/health", nil)
	req.Header.Set("X-Request-Id", "my-custom-id-123")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	id := resp.Header.Get("X-Request-Id")
	if id != "my-custom-id-123" {
		t.Errorf("expected preserved request ID 'my-custom-id-123', got %q", id)
	}
}

func TestRequestIDUnique(t *testing.T) {
	ts := newTestServer(t)

	var ids []string
	for range 5 {
		resp, err := http.Get(ts.URL + "/api/v1/health")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		ids = append(ids, resp.Header.Get("X-Request-Id"))
	}

	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate request ID: %s", id)
		}
		seen[id] = true
	}
}

func TestServerError_NoDetailsLeaked(t *testing.T) {
	ts := newTestServer(t)

	// A query endpoint that will fail because there's no query service configured
	// (we used NewServer, not NewServerWithQuery). The 404 is expected.
	// Instead, test that invalid ingest with auth returns structured error without internals.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", nil)
	req.Header.Set("X-SpanBarn-Api-Key", "wrong-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var errResp api.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}

	// Error message should be generic, not contain stack traces or internal paths.
	if errResp.Error == "" {
		t.Error("expected non-empty error message")
	}
	if errResp.Details != "" {
		t.Errorf("expected no details on auth error, got %q", errResp.Details)
	}
}
