package api_test

import (
	"bytes"
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

// Ingest authenticates with its API key, so it keeps working on an instance
// whose OIDC configuration refuses every token.
func TestIngestDoesNotDependOnOIDCOrganization(t *testing.T) {
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })
	h := ingest.NewHandler(ingest.NewQueue(1024), sp, 0, slog.Default())
	h.Start(t.Context())
	t.Cleanup(func() { h.Stop() })

	srv := api.NewServer(api.ServerConfig{APIKey: testAPIKey, MaxBodyBytes: 4 << 20, Version: "test"}, h, slog.Default())
	srv.SetOIDCClient(auth.NewOIDCClient(auth.OIDCConfig{
		Issuer:       "https://iam.example.com",
		ClientID:     "spanbarn",
		ClientSecret: "secret",
		RedirectURL:  "https://spanbarn.example.com/api/v1/oidc/callback",
	}))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body, _ := json.Marshal(api.SpanBatch{Spans: []api.SpanInput{{
		TraceID: "abc123", SpanID: "span1", Name: "GET /users", Service: "api", StartTime: 1700000000000, Duration: 5000,
	}}})
	post := func(key string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("X-SpanBarn-Api-Key", key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := post(testAPIKey); code != http.StatusAccepted {
		t.Errorf("with the key: status %d, want 202", code)
	}
	if code := post(""); code != http.StatusUnauthorized {
		t.Errorf("without a key: status %d, want 401", code)
	}
}
