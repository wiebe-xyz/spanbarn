package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// sessionTestStack wires a SessionService to the fake IdP and returns the
// middleware-wrapped probe handler used by the flow tests.
func sessionTestStack(t *testing.T) (*fakeIdP, *SessionService, *repository.Repository, http.Handler) {
	t.Helper()
	idp := newFakeIdP(t)
	svc, repo := newTestSessions(t)
	oc := idp.client()
	svc.SetOIDCProvider(func() *auth.OIDCClient { return oc })

	handler := SessionMiddleware(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if GetUsername(r.Context()) == "" {
			// The middleware must always populate the username.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	return idp, svc, repo, handler
}

func doSession(handler http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestSessionRefreshOnExpiryUpdatesRow: an OIDC session with an expired
// access token is transparently refreshed on the next request; the row gets
// the rotated tokens, the new expiry, and a fresh claims snapshot.
func TestSessionRefreshOnExpiryUpdatesRow(t *testing.T) {
	idp, svc, repo, handler := sessionTestStack(t)
	idp.script(0, "at-2", "rt-2", map[string]any{"roles": []string{"owner"}, "name": "Wiebe Renamed"})

	token := newOIDCSessionRow(t, svc, time.Now().Add(-time.Minute), "rt-1")

	if rec := doSession(handler, token); rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after silent refresh, got %d: %s", rec.Code, rec.Body.String())
	}
	if idp.calls() != 1 {
		t.Fatalf("expected 1 token-endpoint call, got %d", idp.calls())
	}

	ws, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token))
	if err != nil {
		t.Fatalf("row gone after refresh: %v", err)
	}
	if ws.AccessToken != "at-2" || ws.RefreshToken != "rt-2" {
		t.Errorf("tokens not rotated: %+v", ws)
	}
	if ws.AccessExpiresAt <= time.Now().Unix() {
		t.Errorf("access_expires_at not advanced: %d", ws.AccessExpiresAt)
	}
	if ws.LastRefreshAt == 0 {
		t.Error("last_refresh_at not stamped")
	}
	if !strings.Contains(ws.ClaimsJSON, "Wiebe Renamed") {
		t.Errorf("claims not re-snapshotted from the refreshed id_token: %s", ws.ClaimsJSON)
	}

	// A fresh token needs no second refresh.
	if rec := doSession(handler, token); rec.Code != http.StatusOK {
		t.Fatalf("second request: %d", rec.Code)
	}
	if idp.calls() != 1 {
		t.Errorf("no further refresh expected, got %d calls", idp.calls())
	}
}

// TestSessionRefreshInvalidGrant401AndRowDeleted: invalid_grant means the
// refresh token is dead (revoked centrally / replayed) — the session dies NOW
// and the row is gone. invalid_grant never gets grace.
func TestSessionRefreshInvalidGrant401AndRowDeleted(t *testing.T) {
	idp, svc, repo, handler := sessionTestStack(t)
	idp.script(http.StatusBadRequest, "", "", nil)

	token := newOIDCSessionRow(t, svc, time.Now().Add(-time.Minute), "rt-dead")

	if rec := doSession(handler, token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on invalid_grant, got %d", rec.Code)
	}
	if _, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("row must be deleted on invalid_grant, got err = %v", err)
	}
}

// TestSessionRefreshTransientFailureServesStaleUntilGrace: IdP 5xx keeps the
// session alive with the stale access token until the grace ceiling, after
// which requests 401.
func TestSessionRefreshTransientFailureServesStaleUntilGrace(t *testing.T) {
	idp := newFakeIdP(t)
	repo := newTestRepo(t)
	svc := NewSessionService(repo, 43200, 3600, slog.Default())
	oc := idp.client()
	svc.SetOIDCProvider(func() *auth.OIDCClient { return oc })
	handler := SessionMiddleware(svc)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	now := time.Now()
	clock := now
	svc.now = func() time.Time { return clock }

	idp.script(http.StatusInternalServerError, "", "", nil)
	token := newOIDCSessionRow(t, svc, now.Add(-time.Minute), "rt-1")

	// Within grace: stale-served.
	if rec := doSession(handler, token); rec.Code != http.StatusOK {
		t.Fatalf("expected stale-serve within grace, got %d", rec.Code)
	}
	ws, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token))
	if err != nil {
		t.Fatalf("row must survive a transient failure: %v", err)
	}
	if ws.RefreshFailingSince == 0 {
		t.Fatal("refresh_failing_since must be stamped on first transient failure")
	}

	// Still within grace 30 minutes later.
	clock = now.Add(30 * time.Minute)
	if rec := doSession(handler, token); rec.Code != http.StatusOK {
		t.Fatalf("expected stale-serve at t+30m, got %d", rec.Code)
	}

	// Past the 1h grace ceiling: 401.
	clock = now.Add(61 * time.Minute)
	if rec := doSession(handler, token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after grace exhausted, got %d", rec.Code)
	}

	// IdP recovers within another session's grace → refresh heals the row.
	idp.script(0, "at-9", "rt-9", nil)
	clock = now.Add(30 * time.Minute)
	token2 := newOIDCSessionRow(t, svc, now.Add(-time.Minute), "rt-1")
	if rec := doSession(handler, token2); rec.Code != http.StatusOK {
		t.Fatalf("expected refresh to succeed after recovery, got %d", rec.Code)
	}
	ws2, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token2))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ws2.RefreshFailingSince != 0 {
		t.Error("successful refresh must clear refresh_failing_since")
	}
}

// TestSessionAbsoluteCap: a session past absolute_expires_at is dead no
// matter how fresh its access token is, and the row is removed.
func TestSessionAbsoluteCap(t *testing.T) {
	_, svc, repo, handler := sessionTestStack(t)

	token := newOIDCSessionRow(t, svc, time.Now().Add(10*time.Minute), "rt-1")
	// Move the clock past the 1h TTL configured by newTestSessions.
	svc.now = func() time.Time { return time.Now().Add(2 * time.Hour) }

	if rec := doSession(handler, token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 past absolute cap, got %d", rec.Code)
	}
	if _, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired row must be deleted, got err = %v", err)
	}
}

// TestSessionRefreshSingleflight: concurrent requests over one expired
// session must produce exactly one token-endpoint call — a second refresh
// with the same single-use token would be replay-revoked by iambarn.
func TestSessionRefreshSingleflight(t *testing.T) {
	idp, svc, _, handler := sessionTestStack(t)
	idp.script(0, "at-2", "rt-2", nil)

	token := newOIDCSessionRow(t, svc, time.Now().Add(-time.Minute), "rt-1")

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i] = doSession(handler, token).Code
		}(i)
	}
	close(start)
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("request %d: got %d", i, code)
		}
	}
	if idp.calls() != 1 {
		t.Errorf("expected exactly 1 refresh across %d concurrent requests, got %d", n, idp.calls())
	}
}

// TestSessionLocalNeverRefreshes: local/e2e sessions have no IdP tokens and
// must never touch the token endpoint.
func TestSessionLocalNeverRefreshes(t *testing.T) {
	idp, svc, _, handler := sessionTestStack(t)
	token := mintSession(t, svc, "admin")

	if rec := doSession(handler, token); rec.Code != http.StatusOK {
		t.Fatalf("local session: %d", rec.Code)
	}
	if idp.calls() != 0 {
		t.Errorf("local session must not refresh, got %d calls", idp.calls())
	}
}

// TestSessionReadOnlyStoreServesStaleWithRefreshDueHeader: reader pods mount
// SQLite read-only; the middleware must not spend the single-use refresh
// token there. It serves stale within grace and flags X-Session-Refresh-Due
// so the SPA triggers the writer-routed refresh.
func TestSessionReadOnlyStoreServesStaleWithRefreshDueHeader(t *testing.T) {
	idp := newFakeIdP(t)
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rwRepo := repository.NewRepository(db.DB)
	roRepo := repository.NewReadOnlyRepository(db.DB)

	writerSvc := NewSessionService(rwRepo, 43200, 3600, nil)
	readerSvc := NewSessionService(roRepo, 43200, 3600, nil)
	oc := idp.client()
	readerSvc.SetOIDCProvider(func() *auth.OIDCClient { return oc })
	handler := SessionMiddleware(readerSvc)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token := newOIDCSessionRow(t, writerSvc, time.Now().Add(-time.Minute), "rt-1")

	rec := doSession(handler, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected stale-serve on read-only store, got %d", rec.Code)
	}
	if rec.Header().Get("X-Session-Refresh-Due") != "1" {
		t.Error("expected X-Session-Refresh-Due header on stale-served response")
	}
	if idp.calls() != 0 {
		t.Errorf("read-only replica must never call the token endpoint, got %d", idp.calls())
	}

	// Beyond access expiry + grace the reader rejects too.
	readerSvc.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if rec := doSession(handler, token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 past reader grace, got %d", rec.Code)
	}
}

// TestBackchannelLogoutBySidAndSub exercises POST /api/v1/oidc/backchannel-logout
// end to end against signed logout tokens: kill by sid, kill by sub, and the
// validation rejections (nonce, missing events, garbage).
func TestBackchannelLogoutBySidAndSub(t *testing.T) {
	idp := newFakeIdP(t)
	svc, repo := newTestSessions(t)
	oc := idp.client()
	svc.SetOIDCProvider(func() *auth.OIDCClient { return oc })
	s := &Server{logger: slog.Default(), oidc: oc, sessions: svc}

	logoutClaims := func(mut func(map[string]any)) map[string]any {
		c := map[string]any{
			"iss": idp.srv.URL,
			"aud": "spanbarn-web",
			"sub": "sub-1",
			"sid": "sid-1",
			"iat": time.Now().Unix(),
			"exp": time.Now().Add(2 * time.Minute).Unix(),
			"jti": "jti-1",
			"events": map[string]any{
				"http://schemas.openid.net/event/backchannel-logout": map[string]any{},
			},
		}
		if mut != nil {
			mut(c)
		}
		return c
	}
	post := func(logoutToken string) *httptest.ResponseRecorder {
		form := url.Values{"logout_token": {logoutToken}}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/oidc/backchannel-logout",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		s.handleBackchannelLogout(rec, req)
		return rec
	}

	// Two sessions of sub-1 with different sids + one other subject.
	tokA := newOIDCSessionRow(t, svc, time.Now().Add(10*time.Minute), "rt-a") // sid-1
	tokB, _, err := svc.Create("Wiebe", "oidc", &OIDCSessionData{
		Claims:          auth.OIDCClaims{Subject: "sub-1", SessionID: "sid-2", Roles: []string{"owner"}},
		AccessToken:     "at-b",
		RefreshToken:    "rt-b",
		AccessExpiresAt: time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create session B: %v", err)
	}
	tokC, _, err := svc.Create("Other", "oidc", &OIDCSessionData{
		Claims:          auth.OIDCClaims{Subject: "sub-2", SessionID: "sid-3", Roles: []string{"owner"}},
		AccessToken:     "at-c",
		RefreshToken:    "rt-c",
		AccessExpiresAt: time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create session C: %v", err)
	}

	// Kill by sid: only session A dies.
	if rec := post(idp.sign(t, logoutClaims(nil))); rec.Code != http.StatusOK {
		t.Fatalf("by-sid: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(tokA)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("session with sid-1 must be gone")
	}
	if _, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(tokB)); err != nil {
		t.Fatal("session with sid-2 must survive a sid-1 logout")
	}

	// Kill by sub (no sid): every remaining sub-1 session dies; sub-2 survives.
	if rec := post(idp.sign(t, logoutClaims(func(c map[string]any) { delete(c, "sid") }))); rec.Code != http.StatusOK {
		t.Fatalf("by-sub: expected 200, got %d", rec.Code)
	}
	if _, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(tokB)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("session with sub-1 must be gone after by-sub logout")
	}
	if _, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(tokC)); err != nil {
		t.Fatal("other subject must survive")
	}

	// Validation failures → 400.
	for name, tok := range map[string]string{
		"nonce present":  idp.sign(t, logoutClaims(func(c map[string]any) { c["nonce"] = "n" })),
		"missing events": idp.sign(t, logoutClaims(func(c map[string]any) { delete(c, "events") })),
		"garbage":        "not-a-jwt",
	} {
		if rec := post(tok); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, rec.Code)
		}
	}
	if rec := post(""); rec.Code != http.StatusBadRequest {
		t.Errorf("missing logout_token: expected 400, got %d", rec.Code)
	}
}

// TestLogoutRevokesAndReturnsEndSessionURL: server-driven logout revokes the
// refresh token at the issuer, deletes the row, and hands the SPA the
// end-session URL with id_token_hint.
func TestLogoutRevokesAndReturnsEndSessionURL(t *testing.T) {
	idp := newFakeIdP(t)
	svc, repo := newTestSessions(t)
	oc := idp.client()
	svc.SetOIDCProvider(func() *auth.OIDCClient { return oc })

	token := newOIDCSessionRow(t, svc, time.Now().Add(10*time.Minute), "rt-1")

	handler := HandleLogout(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/logout", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	u, err := url.Parse(resp["logout_url"])
	if err != nil || u.Path != "/oauth2/end-session" {
		t.Fatalf("logout_url = %q", resp["logout_url"])
	}
	if u.Query().Get("id_token_hint") != "idtok-login" {
		t.Errorf("id_token_hint = %q", u.Query().Get("id_token_hint"))
	}
	if u.Query().Get("client_id") != "spanbarn-web" {
		t.Errorf("client_id = %q", u.Query().Get("client_id"))
	}
	if u.Query().Get("post_logout_redirect_uri") == "" {
		t.Error("post_logout_redirect_uri missing")
	}

	// Row deleted; revocation endpoint saw the refresh token.
	if _, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("session row must be deleted on logout")
	}
	idp.mu.Lock()
	revoked := idp.lastTokenForm["token"]
	idp.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "rt-1" {
		t.Errorf("refresh token not revoked at issuer: %v", revoked)
	}

	// Local sessions: no logout_url, still 200 + cookie cleared.
	localTok := mintSession(t, svc, "admin")
	req = httptest.NewRequest(http.MethodPost, "/api/v1/logout", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: localTok})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local logout: %d", rec.Code)
	}
	var localResp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &localResp)
	if localResp["logout_url"] != "" {
		t.Errorf("local logout must not return a logout_url, got %q", localResp["logout_url"])
	}
}
