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
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

// newExtSessionService builds a SessionService over an in-memory repository
// for tests living outside the api package.
func newExtSessionService(t *testing.T) *api.SessionService {
	t.Helper()
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return api.NewSessionService(repository.NewRepository(db.DB), 3600, 3600, nil)
}

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
	if body.IAMBarn.ProfileURL != "https://iam.example.com/admin#profile" {
		t.Fatalf("unexpected profile_url: %q", body.IAMBarn.ProfileURL)
	}
}

func TestClientConfigIAMBarnWidgetFields(t *testing.T) {
	srv := newServer(t, "1.0.0")
	srv.SetOIDCClient(auth.NewOIDCClient(auth.OIDCConfig{
		Issuer:                "https://iam.example.com/",
		ClientID:              "spanbarn-web",
		ClientSecret:          "secret",
		RedirectURL:           "https://spanbarn.example.com/api/v1/oidc/callback",
		PostLogoutRedirectURI: "https://spanbarn.example.com/api/v1/oidc/logout-complete",
	}))

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/client-config")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		IAMBarn struct {
			ClientID              string `json:"client_id"`
			PostLogoutRedirectURI string `json:"post_logout_redirect_uri"`
		} `json:"iambarn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.IAMBarn.ClientID != "spanbarn-web" {
		t.Fatalf("client_id = %q, want spanbarn-web", body.IAMBarn.ClientID)
	}
	if body.IAMBarn.PostLogoutRedirectURI != "https://spanbarn.example.com/api/v1/oidc/logout-complete" {
		t.Fatalf("post_logout_redirect_uri = %q", body.IAMBarn.PostLogoutRedirectURI)
	}
}

func TestOIDCLogoutComplete(t *testing.T) {
	srv := newServer(t, "1.0.0")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(ts.URL + "/api/v1/oidc/logout-complete")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Fatalf("expected redirect to /login, got %q", loc)
	}
	cleared := map[string]bool{"session": false, "spanbarn_auth_method": false, "spanbarn_iam_token": false}
	for _, c := range resp.Cookies() {
		if _, ok := cleared[c.Name]; ok && c.MaxAge < 0 {
			cleared[c.Name] = true
		}
	}
	for name, ok := range cleared {
		if !ok {
			t.Fatalf("cookie %q was not cleared", name)
		}
	}
}

func TestMeEndpoint(t *testing.T) {
	sessions := newExtSessionService(t)
	token, _, err := sessions.Create("alice", "local", nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	srv := newServerWithSession(t, sessions)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.DisplayName != "alice" {
		t.Fatalf("unexpected display_name: %q", body.DisplayName)
	}
}

func newServerWithSession(t *testing.T, sessions *api.SessionService) *api.Server {
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
	return api.NewServerWithQuery(api.ServerConfig{APIKey: testAPIKey, Version: "1.0.0"}, h, nil, sessions, slog.Default())
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
