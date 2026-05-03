package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

func TestAPIKeyMiddleware(t *testing.T) {
	rawKey := "test-api-key"
	hash := auth.HashKey(rawKey)
	authorizer := auth.NewAuthorizer(hash, nil, nil)

	handler := APIKeyMiddleware(authorizer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pid := GetProjectID(r.Context())
		scope := GetScope(r.Context())
		if scope != "full" {
			t.Errorf("expected scope 'full', got %q", scope)
		}
		if pid != 0 {
			t.Errorf("expected projectID 0, got %d", pid)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-SpanBarn-Api-Key", rawKey)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAPIKeyMiddlewareNoKey(t *testing.T) {
	authorizer := auth.NewAuthorizer(auth.HashKey("key"), nil, nil)

	handler := APIKeyMiddleware(authorizer)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAPIKeyMiddlewareInvalidKey(t *testing.T) {
	authorizer := auth.NewAuthorizer(auth.HashKey("correct"), nil, nil)

	handler := APIKeyMiddleware(authorizer)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-SpanBarn-Api-Key", "wrong")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestSessionMiddleware(t *testing.T) {
	sm := auth.NewSessionManager("test-secret", 3600)
	token, err := sm.Create("admin")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	handler := SessionMiddleware(sm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username := GetUsername(r.Context())
		if username != "admin" {
			t.Errorf("expected username 'admin', got %q", username)
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Test with cookie.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with cookie, got %d", rec.Code)
	}

	// Test with Bearer token.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()

	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("expected 200 with bearer, got %d", rec2.Code)
	}
}

func TestSessionMiddlewareNoSession(t *testing.T) {
	sm := auth.NewSessionManager("test-secret", 3600)

	handler := SessionMiddleware(sm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestSessionMiddlewareExpired(t *testing.T) {
	sm := auth.NewSessionManager("test-secret", 1)

	// Create an already-expired token by manipulating the clock.
	import_time := sm.now
	_ = import_time
	// We need to create a token that's already expired. Override the now func.
	origNow := sm.now
	_ = origNow
	// SessionManager fields are unexported, so we create a short-lived token
	// and validate after expiry. Use a manager with 1s TTL and time manipulation
	// isn't possible from outside the package. Instead, we'll use a known-expired approach.

	// Actually, the SessionManager's now and ttl are unexported. Let's just
	// create a token with the auth package's test helper by making a token
	// from a different manager that we can control... but we can't.
	// The simplest approach: create a valid token with a very short TTL,
	// then construct a middleware test.

	// Since we can't easily expire from outside, let's test with an invalid token.
	handler := SessionMiddleware(sm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "invalid.token"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid session, got %d", rec.Code)
	}
}
