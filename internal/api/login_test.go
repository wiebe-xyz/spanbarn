package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

// testUserLookup implements auth.UserLookup for testing.
type testUserLookup struct {
	users map[string]auth.UserRecord
}

func (m *testUserLookup) GetUserByUsername(username string) (auth.UserRecord, error) {
	if u, ok := m.users[username]; ok {
		return u, nil
	}
	return auth.UserRecord{}, auth.ErrInvalidCredentials
}

func setupLoginTest(t *testing.T) (*auth.UserAuthenticator, *SessionService) {
	t.Helper()

	hash, err := auth.HashPassword("correct-pass")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	repo := &testUserLookup{
		users: map[string]auth.UserRecord{
			"admin": {ID: 1, Username: "admin", PasswordHash: hash},
		},
	}

	userAuth := auth.NewUserAuthenticator(repo, nil)
	sm, _ := newTestSessions(t)
	return userAuth, sm
}

func TestLoginSuccess(t *testing.T) {
	userAuth, sm := setupLoginTest(t)
	handler := HandleLogin(userAuth, sm, nil, nil)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "correct-pass"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Check that a session cookie was set.
	cookies := rec.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == "session" && c.Value != "" {
			found = true
			if !c.HttpOnly {
				t.Error("session cookie should be HttpOnly")
			}
			break
		}
	}
	if !found {
		t.Error("expected session cookie to be set")
	}

	// Check response body.
	var resp loginResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Username != "admin" {
		t.Errorf("expected username 'admin', got %q", resp.Username)
	}
}

// TestLoginCookieSecureOnForwardedProto verifies the session cookie is marked
// Secure when the request arrived over HTTPS at the proxy (X-Forwarded-Proto),
// which is the only signal available since TLS terminates upstream.
func TestLoginCookieSecureOnForwardedProto(t *testing.T) {
	userAuth, sm := setupLoginTest(t)
	handler := HandleLogin(userAuth, sm, nil, nil)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "correct-pass"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			found = true
			if !c.Secure {
				t.Error("session cookie should be Secure when X-Forwarded-Proto is https")
			}
		}
	}
	if !found {
		t.Fatal("expected session cookie to be set")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	userAuth, sm := setupLoginTest(t)
	handler := HandleLogin(userAuth, sm, nil, nil)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "wrong-pass"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestLoginAccountThrottle verifies the per-username limiter bounds attempts
// against one account regardless of source IP: with a 2/min account limit, the
// third attempt is refused with 429 even before password checking.
func TestLoginAccountThrottle(t *testing.T) {
	userAuth, sm := setupLoginTest(t)
	limiter := NewRateLimiter(2 /*login*/, 1000, 1000)
	handler := HandleLogin(userAuth, sm, limiter, nil)

	do := func() int {
		body, _ := json.Marshal(loginRequest{Username: "admin", Password: "wrong-pass"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if c := do(); c != http.StatusUnauthorized {
		t.Fatalf("attempt 1: want 401, got %d", c)
	}
	if c := do(); c != http.StatusUnauthorized {
		t.Fatalf("attempt 2: want 401, got %d", c)
	}
	if c := do(); c != http.StatusTooManyRequests {
		t.Fatalf("attempt 3: want 429 (account throttled), got %d", c)
	}
}

func TestLoginMethodNotAllowed(t *testing.T) {
	userAuth, sm := setupLoginTest(t)
	handler := HandleLogin(userAuth, sm, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/login", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestLogout(t *testing.T) {
	svc, repo := newTestSessions(t)
	token := mintSession(t, svc, "admin")
	handler := HandleLogout(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/logout", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	cookies := rec.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == "session" {
			found = true
			if c.MaxAge != -1 {
				t.Errorf("expected MaxAge=-1, got %d", c.MaxAge)
			}
			break
		}
	}
	if !found {
		t.Error("expected session cookie to be cleared")
	}

	// The server-side row must be gone: the cookie clearing is cosmetic, the
	// row deletion is the actual revocation.
	if _, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token)); err == nil {
		t.Error("expected session row to be deleted on logout")
	}

	// No legacy raw-token cookie may be (re)issued with a value.
	for _, c := range cookies {
		if (c.Name == "spanbarn_iam_token" || c.Name == "spanbarn_iam_refresh") && c.Value != "" {
			t.Errorf("legacy cookie %q must never carry a value, got %q", c.Name, c.Value)
		}
	}
}

// TestLoginCreatesServerSideRow: the login response cookie is only a handle;
// the authoritative session lives in web_sessions with auth_method "local".
func TestLoginCreatesServerSideRow(t *testing.T) {
	hash, err := auth.HashPassword("correct-pass")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	userAuth := auth.NewUserAuthenticator(&testUserLookup{
		users: map[string]auth.UserRecord{"admin": {ID: 1, Username: "admin", PasswordHash: hash}},
	}, nil)
	svc, repo := newTestSessions(t)
	handler := HandleLogin(userAuth, svc, nil, nil)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "correct-pass"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var token string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			token = c.Value
		}
	}
	ws, err := repo.GetWebSessionByIDHash(auth.HashSessionToken(token))
	if err != nil {
		t.Fatalf("session row missing: %v", err)
	}
	if ws.AuthMethod != "local" || ws.Username != "admin" {
		t.Errorf("unexpected row: %+v", ws)
	}
	if ws.AccessToken != "" || ws.RefreshToken != "" {
		t.Errorf("local session must have no IdP tokens: %+v", ws)
	}
}
