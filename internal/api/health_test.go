package api_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/auth"
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

func TestClientConfigOIDCNotSet(t *testing.T) {
	srv := newServer(t, "1.0.0")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/client-config")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["oidc"]; ok {
		t.Fatalf("oidc block should be omitted when not configured: %v", body)
	}
	if _, ok := body["iambarn"]; ok {
		t.Fatalf("iambarn block should be omitted when oidc is not configured: %v", body)
	}
}

func TestClientConfigIAMBarnProfileURL(t *testing.T) {
	srv := newServer(t, "1.0.0")
	srv.SetOIDCClient(auth.NewOIDCClient(auth.OIDCConfig{
		Issuer:       "https://iam.example.com/",
		ClientID:     "spanbarn",
		ClientSecret: "secret",
		RedirectURL:  "https://spanbarn.example.com/api/v1/oidc/callback",
	}))

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/client-config")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		OIDC    map[string]any `json:"oidc"`
		IAMBarn struct {
			ProfileURL string `json:"profile_url"`
		} `json:"iambarn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.OIDC == nil {
		t.Fatalf("oidc block should be present")
	}
	if body.IAMBarn.ProfileURL != "https://iam.example.com/admin" {
		t.Fatalf("unexpected profile_url: %q", body.IAMBarn.ProfileURL)
	}
}

func newServer(t *testing.T, version string) *api.Server {
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

	return api.NewServer(api.ServerConfig{APIKey: testAPIKey, Version: version}, h, slog.Default())
}
