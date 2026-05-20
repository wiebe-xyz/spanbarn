package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Manifest mirrors the iambarn theme.Manifest contract the endpoint must
// satisfy. Duplicated here (rather than imported) so this test fails loudly if
// the on-the-wire shape ever drifts from what IAMBarn's fetcher decodes into.
type themeManifest struct {
	Name            string `json:"name"`
	LogoURL         string `json:"logo_url"`
	PrimaryColor    string `json:"primary_color"`
	BackgroundColor string `json:"background_color"`
	CardColor       string `json:"card_color"`
	BodyTextColor   string `json:"body_text_color"`
	SupportURL      string `json:"support_url"`
	Locale          string `json:"locale"`
}

func TestIAMBarnThemeManifestEndpoint(t *testing.T) {
	srv := newServer(t, "1.0.0")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Use a client that does not follow redirects — IAMBarn fetches this
	// resource with the same constraint and treats any 3xx as a hard failure.
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/.well-known/iambarn-theme.json", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("expected Content-Type to contain application/json, got %q", ct)
	}

	var m themeManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Name == "" {
		t.Fatalf("expected name to be populated, got empty manifest: %+v", m)
	}
}
