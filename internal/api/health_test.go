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

func TestHealthEndpoint(t *testing.T) {
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
		Version: "1.2.3",
	}, h, slog.Default())

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result api.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", result.Status)
	}
	if result.Version != "1.2.3" {
		t.Fatalf("expected version '1.2.3', got %q", result.Version)
	}
}
