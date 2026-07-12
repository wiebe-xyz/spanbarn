package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

// callbackRequest builds a callback request carrying the state|verifier and
// nonce cookies the login handler would have set.
func callbackRequest(state, verifier, nonce string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/oidc/callback?code=code-1&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: state + "|" + verifier})
	req.AddCookie(&http.Cookie{Name: oidcNonceCookie, Value: nonce})
	return req
}

// TestOIDCCallbackPersistsSessionRowNotTokens is the core of the BFF design:
// the callback stores the whole IamBarn token set server-side and hands the
// browser ONLY the opaque session handle — no raw token cookie ever again.
func TestOIDCCallbackPersistsSessionRowNotTokens(t *testing.T) {
	idp := newFakeIdP(t)
	svc, repo := newTestSessions(t)
	oc := idp.client()
	s := &Server{logger: slog.Default(), oidc: oc, sessions: svc}

	idp.script(0, "at-1", "rt-1", map[string]any{"nonce": "nonce-1"})

	verifier := oauth2.GenerateVerifier()
	rec := httptest.NewRecorder()
	s.handleOIDCCallback(rec, callbackRequest("state-1", verifier, "nonce-1"))

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", rec.Code, rec.Body.String())
	}

	// PKCE: the code exchange must carry the verifier from the state cookie.
	idp.mu.Lock()
	sentVerifier := idp.lastTokenForm["code_verifier"]
	idp.mu.Unlock()
	if len(sentVerifier) != 1 || sentVerifier[0] != verifier {
		t.Errorf("code_verifier not sent on exchange: %v", sentVerifier)
	}

	var sessionValue string
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case "session":
			sessionValue = c.Value
			if !c.HttpOnly {
				t.Error("session cookie must be HttpOnly")
			}
		case legacyIAMAccessCookie, legacyIAMRefreshCookie, legacyIAMProfileCookie:
			if c.Value != "" {
				t.Errorf("raw-token/profile cookie %q must never be set, got value %q", c.Name, c.Value)
			}
		}
	}
	if sessionValue == "" {
		t.Fatal("session cookie not set")
	}
	if strings.Contains(sessionValue, ".") {
		t.Errorf("session cookie should be an opaque handle, got %q", sessionValue)
	}

	ws, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(sessionValue))
	if err != nil {
		t.Fatalf("session row not persisted: %v", err)
	}
	if ws.AuthMethod != "oidc" {
		t.Errorf("auth_method = %q", ws.AuthMethod)
	}
	if ws.AccessToken != "at-1" || ws.RefreshToken != "rt-1" || ws.IDToken == "" {
		t.Errorf("token set not persisted: %+v", ws)
	}
	if ws.IdpSub != "sub-1" || ws.IdpSid != "sid-1" {
		t.Errorf("sub/sid not persisted: sub=%q sid=%q", ws.IdpSub, ws.IdpSid)
	}
	if !strings.Contains(ws.ClaimsJSON, `"email":"wiebe@wiebe.xyz"`) {
		t.Errorf("claims snapshot missing: %s", ws.ClaimsJSON)
	}
	if ws.AccessExpiresAt == 0 {
		t.Error("access_expires_at not persisted")
	}
}

// TestOIDCCallbackRejectsMissingVerifier: a state cookie without the PKCE
// verifier segment (pre-upgrade cookie or tampering) must not reach the code
// exchange.
func TestOIDCCallbackRejectsMissingVerifier(t *testing.T) {
	idp := newFakeIdP(t)
	svc, _ := newTestSessions(t)
	oc := idp.client()
	s := &Server{logger: slog.Default(), oidc: oc, sessions: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/oidc/callback?code=code-1&state=state-1", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: "state-1"}) // no |verifier
	req.AddCookie(&http.Cookie{Name: oidcNonceCookie, Value: "nonce-1"})
	rec := httptest.NewRecorder()
	s.handleOIDCCallback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without verifier, got %d", rec.Code)
	}
	if idp.calls() != 0 {
		t.Errorf("token endpoint must not be called, got %d calls", idp.calls())
	}
}

// TestOIDCCallbackRejectsStateMismatch guards the CSRF check.
func TestOIDCCallbackRejectsStateMismatch(t *testing.T) {
	idp := newFakeIdP(t)
	svc, _ := newTestSessions(t)
	oc := idp.client()
	s := &Server{logger: slog.Default(), oidc: oc, sessions: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/oidc/callback?code=code-1&state=evil", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: "state-1|" + oauth2.GenerateVerifier()})
	req.AddCookie(&http.Cookie{Name: oidcNonceCookie, Value: "nonce-1"})
	rec := httptest.NewRecorder()
	s.handleOIDCCallback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on state mismatch, got %d", rec.Code)
	}
}

// TestOIDCLoginSetsPKCEStateCookie: the login redirect must carry the S256
// challenge and stash state|verifier in one HttpOnly cookie.
func TestOIDCLoginSetsPKCEStateCookie(t *testing.T) {
	idp := newFakeIdP(t)
	svc, _ := newTestSessions(t)
	oc := idp.client()
	s := &Server{logger: slog.Default(), oidc: oc, sessions: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/oidc/login", nil)
	rec := httptest.NewRecorder()
	s.handleOIDCLogin(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "code_challenge=") || !strings.Contains(loc, "code_challenge_method=S256") {
		t.Errorf("authorize URL missing PKCE challenge: %s", loc)
	}

	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcStateCookie {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("state cookie not set")
	}
	parts := strings.SplitN(stateCookie.Value, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("state cookie must be state|verifier, got %q", stateCookie.Value)
	}
	if !strings.Contains(loc, "state="+parts[0]) {
		t.Errorf("authorize URL state %q does not match cookie %q", loc, parts[0])
	}
}
