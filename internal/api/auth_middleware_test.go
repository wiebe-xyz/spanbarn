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
	// Create a token signed by one secret and validate with a different manager,
	// or use an expired token. Since the now field is unexported, we use
	// auth.NewExpiredToken helper to get a pre-expired token.
	sm := auth.NewSessionManager("test-secret", 3600)
	expiredToken := auth.MakeExpiredToken("test-secret", "admin")

	handler := SessionMiddleware(sm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: expiredToken})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired session, got %d", rec.Code)
	}
}
