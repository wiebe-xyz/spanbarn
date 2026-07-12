package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// iamProxyStack builds a server whose /api/iam-proxy/ route is wrapped in the
// session middleware, exactly like registerRoutes does.
func iamProxyStack(t *testing.T) (*fakeIdP, *SessionService, *repository.Repository, http.Handler) {
	t.Helper()
	idp := newFakeIdP(t)
	svc, repo := newTestSessions(t)
	oc := idp.client()
	svc.SetOIDCProvider(func() *auth.OIDCClient { return oc })
	s := &Server{logger: slog.Default(), oidc: oc, sessions: svc}
	handler := SessionMiddleware(svc)(http.HandlerFunc(s.handleIAMProxy))
	return idp, svc, repo, handler
}

func proxyRequest(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/iam-proxy/api/v1/me", nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "session", Value: token})
	}
	return req
}

// TestIAMProxyForwardsSessionAccessToken: the proxy authenticates upstream
// with the access token from the caller's session row.
func TestIAMProxyForwardsSessionAccessToken(t *testing.T) {
	idp, svc, _, handler := iamProxyStack(t)
	idp.mu.Lock()
	idp.meAccess = "at-login" // upstream accepts the login-time token
	idp.mu.Unlock()

	token := newOIDCSessionRow(t, svc, time.Now().Add(10*time.Minute), "rt-1")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, proxyRequest(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if idp.calls() != 0 {
		t.Errorf("no refresh expected for a live token, got %d", idp.calls())
	}
}

// TestIAMProxyRefreshesOnUpstream401: when IamBarn rejects the stored access
// token (revoked/expired upstream before our bookkeeping noticed), the proxy
// runs the shared refresh path once, persists the rotation, and retries —
// the caller never sees the 401.
func TestIAMProxyRefreshesOnUpstream401(t *testing.T) {
	idp, svc, repo, handler := iamProxyStack(t)
	// Upstream no longer accepts at-login; a refresh yields at-2 which it does.
	idp.script(0, "at-2", "rt-2", nil)

	token := newOIDCSessionRow(t, svc, time.Now().Add(10*time.Minute), "rt-1")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, proxyRequest(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected silent refresh + retry to yield 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if idp.calls() != 1 {
		t.Errorf("expected exactly 1 refresh call, got %d", idp.calls())
	}

	// The rotation must be persisted on the session row.
	ws, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token))
	if err != nil {
		t.Fatalf("session row gone: %v", err)
	}
	if ws.AccessToken != "at-2" || ws.RefreshToken != "rt-2" {
		t.Errorf("rotation not persisted: %+v", ws)
	}
}

// TestIAMProxy401AndRowDeletedOnInvalidGrant: a dead refresh token surfaces
// as a clean 401 and the session row is destroyed — the SPA lands on the
// "sign in again" screen.
func TestIAMProxy401AndRowDeletedOnInvalidGrant(t *testing.T) {
	idp, svc, repo, handler := iamProxyStack(t)
	idp.script(http.StatusBadRequest, "", "", nil)

	token := newOIDCSessionRow(t, svc, time.Now().Add(10*time.Minute), "rt-dead")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, proxyRequest(token))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (dead refresh token), got %d", rec.Code)
	}
	if _, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session row must be deleted on invalid_grant, got err = %v", err)
	}
}

// TestIAMProxyLocalSessionHasNoToken: local-password sessions carry no IdP
// tokens; the proxy must reject them without touching the token endpoint.
func TestIAMProxyLocalSessionHasNoToken(t *testing.T) {
	idp, svc, _, handler := iamProxyStack(t)
	token := mintSession(t, svc, "admin")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, proxyRequest(token))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a local session, got %d", rec.Code)
	}
	if idp.calls() != 0 {
		t.Errorf("no refresh attempt expected, got %d", idp.calls())
	}
}

// TestIAMProxyPathScope: only the widget API namespace may be proxied.
func TestIAMProxyPathScope(t *testing.T) {
	_, svc, _, handler := iamProxyStack(t)
	token := newOIDCSessionRow(t, svc, time.Now().Add(10*time.Minute), "rt-1")

	req := httptest.NewRequest(http.MethodGet, "/api/iam-proxy/admin/users", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-/api/ path, got %d", rec.Code)
	}
}

// TestIAMProxyConcurrentRequestsShareOneRefresh reproduces two widget API
// calls firing back-to-back right after upstream starts rejecting the stored
// access token. Both requests belong to the same session (single-use refresh
// token); only one token-endpoint call must happen.
func TestIAMProxyConcurrentRequestsShareOneRefresh(t *testing.T) {
	idp, svc, _, handler := iamProxyStack(t)
	idp.script(0, "at-2", "rt-2", nil)

	token := newOIDCSessionRow(t, svc, time.Now().Add(10*time.Minute), "rt-1")

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			<-start
			handler.ServeHTTP(rec, proxyRequest(token))
			codes[i] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, code)
		}
	}
	if got := idp.calls(); got != 1 {
		t.Errorf("expected exactly 1 refresh call across %d concurrent requests, got %d", n, got)
	}
}
