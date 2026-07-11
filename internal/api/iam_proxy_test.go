package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

// newIAMProxyTestIssuer spins up a fake iambarn that serves discovery, a
// refresh_token token endpoint, and /api/v1/me — enough to drive
// handleIAMProxy's silent-refresh path end to end. currentAccess tracks the
// access token /api/v1/me currently accepts; refreshTo/refreshCount let tests
// script and observe the token endpoint.
type iamProxyTestIssuer struct {
	srv *httptest.Server

	mu              sync.Mutex
	currentAccess   string
	nextAccess      string
	nextRefresh     string
	refreshCalls    int32
	refreshRejected bool
	// refreshDelay, when set, holds the token endpoint open briefly so tests
	// can force many concurrent callers to overlap in time before the first
	// one completes — singleflight only collapses calls that are genuinely
	// in flight together, so without this an in-process mock responds fast
	// enough that goroutines can serialize through as separate (still
	// individually valid, non-overlapping) calls.
	refreshDelay time.Duration
}

func newIAMProxyTestIssuer(t *testing.T) *iamProxyTestIssuer {
	t.Helper()
	it := &iamProxyTestIssuer{}
	mux := http.NewServeMux()
	it.srv = httptest.NewServer(mux)
	t.Cleanup(it.srv.Close)
	issuer := it.srv.URL

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/oauth2/authorize",
			"token_endpoint":                        issuer + "/oauth2/token",
			"jwks_uri":                              issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"EdDSA"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&it.refreshCalls, 1)
		if it.refreshDelay > 0 {
			time.Sleep(it.refreshDelay)
		}
		it.mu.Lock()
		defer it.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if it.refreshRejected {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		it.currentAccess = it.nextAccess
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  it.nextAccess,
			"refresh_token": it.nextRefresh,
			"token_type":    "Bearer",
			"expires_in":    900,
		})
	})
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		it.mu.Lock()
		want := it.currentAccess
		it.mu.Unlock()
		got := r.Header.Get("Authorization")
		if got != "Bearer "+want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"username": "wiebe"})
	})
	return it
}

func newIAMProxyTestServer(oc *auth.OIDCClient) *Server {
	return &Server{logger: slog.Default(), oidc: oc}
}

func iamProxyRequest(accessCookie, refreshCookie string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/iam-proxy/api/v1/me", nil)
	if accessCookie != "" {
		req.AddCookie(&http.Cookie{Name: "spanbarn_iam_token", Value: accessCookie})
	}
	if refreshCookie != "" {
		req.AddCookie(&http.Cookie{Name: "spanbarn_iam_refresh", Value: refreshCookie})
	}
	return req
}

func TestHandleIAMProxySilentlyRefreshesExpiredToken(t *testing.T) {
	it := newIAMProxyTestIssuer(t)
	it.currentAccess = "server-side-current-token"
	it.nextAccess = "fresh-access"
	it.nextRefresh = "fresh-refresh"

	oc := auth.NewOIDCClient(auth.OIDCConfig{Issuer: it.srv.URL, ClientID: "spanbarn-web", ClientSecret: "sek", RedirectURL: "https://spanbarn.example.com/cb"})
	s := newIAMProxyTestServer(oc)

	req := iamProxyRequest("expired-access", "old-refresh")
	rec := httptest.NewRecorder()
	s.handleIAMProxy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the caller to never see the 401 (silent refresh), got %d: %s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&it.refreshCalls) != 1 {
		t.Errorf("expected exactly 1 refresh call, got %d", it.refreshCalls)
	}

	cookies := rec.Result().Cookies()
	var gotAccess, gotRefresh string
	for _, c := range cookies {
		switch c.Name {
		case "spanbarn_iam_token":
			gotAccess = c.Value
		case "spanbarn_iam_refresh":
			gotRefresh = c.Value
		}
	}
	if gotAccess != "fresh-access" {
		t.Errorf("access cookie not updated: got %q, want fresh-access", gotAccess)
	}
	if gotRefresh != "fresh-refresh" {
		t.Errorf("refresh cookie not rotated: got %q, want fresh-refresh", gotRefresh)
	}
}

func TestHandleIAMProxyClearsCookiesOnInvalidGrant(t *testing.T) {
	it := newIAMProxyTestIssuer(t)
	it.currentAccess = "server-side-current-token"
	it.refreshRejected = true

	oc := auth.NewOIDCClient(auth.OIDCConfig{Issuer: it.srv.URL, ClientID: "spanbarn-web", ClientSecret: "sek", RedirectURL: "https://spanbarn.example.com/cb"})
	s := newIAMProxyTestServer(oc)

	req := iamProxyRequest("expired-access", "dead-refresh")
	rec := httptest.NewRecorder()
	s.handleIAMProxy(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected a clean 401 (dead refresh token), got %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if (c.Name == "spanbarn_iam_token" || c.Name == "spanbarn_iam_refresh") && c.MaxAge >= 0 {
			t.Errorf("cookie %q should be cleared (MaxAge<0), got MaxAge=%d value=%q", c.Name, c.MaxAge, c.Value)
		}
	}
}

func TestHandleIAMProxyNoRefreshCookieReturns401(t *testing.T) {
	it := newIAMProxyTestIssuer(t)
	it.currentAccess = "server-side-current-token"

	oc := auth.NewOIDCClient(auth.OIDCConfig{Issuer: it.srv.URL, ClientID: "spanbarn-web", ClientSecret: "sek", RedirectURL: "https://spanbarn.example.com/cb"})
	s := newIAMProxyTestServer(oc)

	// No refresh cookie at all (e.g. a session predating offline_access) —
	// must fall straight through to 401, no refresh attempt.
	req := iamProxyRequest("expired-access", "")
	rec := httptest.NewRecorder()
	s.handleIAMProxy(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if atomic.LoadInt32(&it.refreshCalls) != 0 {
		t.Errorf("should not have attempted a refresh with no refresh cookie, got %d calls", it.refreshCalls)
	}
}

// TestHandleIAMProxyConcurrentRequestsShareOneRefresh reproduces two widget
// API calls firing back-to-back right after the access token expires. Both
// present the same (single-use) refresh_token; only one token-endpoint call
// must happen — a second concurrent call with the same refresh_token would
// be replay-rejected by iambarn and revoke the whole token family.
func TestHandleIAMProxyConcurrentRequestsShareOneRefresh(t *testing.T) {
	it := newIAMProxyTestIssuer(t)
	it.currentAccess = "server-side-current-token"
	it.nextAccess = "fresh-access"
	it.nextRefresh = "fresh-refresh"
	// Force every goroutine's refresh attempt to overlap: without this, an
	// in-process mock can respond fast enough that 8 goroutines serialize
	// through as separate (individually valid) calls instead of racing.
	it.refreshDelay = 50 * time.Millisecond

	oc := auth.NewOIDCClient(auth.OIDCConfig{Issuer: it.srv.URL, ClientID: "spanbarn-web", ClientSecret: "sek", RedirectURL: "https://spanbarn.example.com/cb"})
	s := newIAMProxyTestServer(oc)

	const n = 8
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, n)
	ready.Add(n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := iamProxyRequest("expired-access", "old-refresh")
			rec := httptest.NewRecorder()
			ready.Done()
			<-start // all goroutines fire together, maximizing overlap
			s.handleIAMProxy(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	ready.Wait()
	close(start)
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, code)
		}
	}
	if got := atomic.LoadInt32(&it.refreshCalls); got != 1 {
		t.Errorf("expected exactly 1 refresh call across %d concurrent requests, got %d", n, got)
	}
}
