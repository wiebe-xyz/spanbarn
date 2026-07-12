package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

func TestE2ESessionGates(t *testing.T) {
	svc, repo := newTestSessions(t)
	newE2EServer := func(env string, enabled bool) *Server {
		return &Server{logger: slog.Default(), sessions: svc, repo: repo, environment: env, e2eEnabled: enabled}
	}
	post := func(s *Server) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/e2e/session", nil)
		rec := httptest.NewRecorder()
		s.handleE2ESession(rec, req)
		return rec
	}

	// Production hard-block wins even with the flag on.
	if rec := post(newE2EServer("production", true)); rec.Code != http.StatusForbidden {
		t.Fatalf("production: expected 403, got %d", rec.Code)
	}
	// Off by default outside production: SPANBARN_E2E_ENABLED must opt in.
	if rec := post(newE2EServer("testing", false)); rec.Code != http.StatusForbidden {
		t.Fatalf("flag off: expected 403, got %d", rec.Code)
	}

	// Enabled on a non-production tier: creates an e2e session row.
	rec := post(newE2EServer("testing", true))
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var token string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("session cookie not set")
	}
	ws, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token))
	if err != nil {
		t.Fatalf("session row missing: %v", err)
	}
	if ws.AuthMethod != "e2e" || ws.Username != "e2e:admin" {
		t.Errorf("unexpected row: %+v", ws)
	}
}

// TestSessionRefreshEndpoint: the writer-routed POST forces the refresh grant
// and reports the new expiry; unauthenticated calls 401.
func TestSessionRefreshEndpoint(t *testing.T) {
	idp := newFakeIdP(t)
	svc, repo := newTestSessions(t)
	oc := idp.client()
	svc.SetOIDCProvider(func() *auth.OIDCClient { return oc })
	s := &Server{logger: slog.Default(), oidc: oc, sessions: svc}
	idp.script(0, "at-2", "rt-2", nil)

	token := newOIDCSessionRow(t, svc, time.Now().Add(-time.Minute), "rt-1")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	s.handleSessionRefresh(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	ws, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token))
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	if ws.AccessToken != "at-2" {
		t.Errorf("refresh not persisted: %+v", ws)
	}

	// No session → 401; wrong method → 405.
	rec = httptest.NewRecorder()
	s.handleSessionRefresh(rec, httptest.NewRequest(http.MethodPost, "/api/v1/session/refresh", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no session: expected 401, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.handleSessionRefresh(rec, httptest.NewRequest(http.MethodGet, "/api/v1/session/refresh", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: expected 405, got %d", rec.Code)
	}
}
