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

func clientConfigBody(t *testing.T, cfg api.ServerConfig) map[string]any {
	t.Helper()
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })
	h := ingest.NewHandler(ingest.NewQueue(1024), sp, 0, slog.Default())
	h.Start(t.Context())
	t.Cleanup(func() { h.Stop() })

	ts := httptest.NewServer(api.NewServer(cfg, h, slog.Default()).Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/api/v1/client-config")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestClientConfigBrandTraceOmittedWithoutKey(t *testing.T) {
	body := clientConfigBody(t, api.ServerConfig{APIKey: testAPIKey, Version: "1.0.0", BrandTraceSDKURL: "https://bt.example/sdk.js"})
	if _, ok := body["brandtrace"]; ok {
		t.Fatalf("brandtrace block should be omitted when the site key is empty: %v", body)
	}
}

func TestClientConfigBrandTraceCarriesKeyAndSDKURL(t *testing.T) {
	body := clientConfigBody(t, api.ServerConfig{
		APIKey: testAPIKey, Version: "1.0.0",
		BrandTraceSiteKey: "site-key-1", BrandTraceSDKURL: "https://bt.example/sdk.js",
	})
	bt, ok := body["brandtrace"].(map[string]any)
	if !ok {
		t.Fatalf("brandtrace block missing: %v", body)
	}
	if bt["site_key"] != "site-key-1" || bt["sdk_url"] != "https://bt.example/sdk.js" {
		t.Fatalf("unexpected brandtrace block: %v", bt)
	}
}
