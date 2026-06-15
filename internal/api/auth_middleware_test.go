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

// fakeKeyLookup implements auth.KeyLookup, returning a fixed record for a
// single known hash.
type fakeKeyLookup struct {
	hash string
	rec  auth.APIKeyRecord
}

func (f *fakeKeyLookup) GetAPIKeyByHash(keyHash string) (auth.APIKeyRecord, error) {
	if keyHash == f.hash {
		return f.rec, nil
	}
	return auth.APIKeyRecord{}, auth.ErrUnauthorized
}

func (f *fakeKeyLookup) TouchAPIKey(int64) error { return nil }

func TestSessionOrReadKey(t *testing.T) {
	sm := auth.NewSessionManager("test-secret", 3600)
	token, err := sm.Create("admin")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	readKey := "read-key-raw"
	ingestKey := "ingest-key-raw"
	lookup := &fakeKeyLookup{}
	authorizer := auth.NewAuthorizer("", lookup, nil)

	newHandler := func() http.Handler {
		return SessionOrReadKey(sm, authorizer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = r
		}))
	}

	t.Run("read key allows GET and scopes project", func(t *testing.T) {
		lookup.hash = auth.HashKey(readKey)
		lookup.rec = auth.APIKeyRecord{ID: 1, ProjectID: 7, Scope: "read"}

		var gotPID int64
		var gotQueryPID string
		handler := SessionOrReadKey(sm, authorizer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPID = GetProjectID(r.Context())
			gotQueryPID = r.URL.Query().Get("project_id")
			w.WriteHeader(http.StatusOK)
		}))

		// Client tries to target another project; the key must override it.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/traces?project_id=99", nil)
		req.Header.Set("X-SpanBarn-Api-Key", readKey)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if gotPID != 7 {
			t.Errorf("expected ctx projectID 7, got %d", gotPID)
		}
		if gotQueryPID != "7" {
			t.Errorf("expected project_id query overridden to 7, got %q", gotQueryPID)
		}
	})

	t.Run("read key rejects non-GET", func(t *testing.T) {
		lookup.hash = auth.HashKey(readKey)
		lookup.rec = auth.APIKeyRecord{ID: 1, ProjectID: 7, Scope: "read"}

		req := httptest.NewRequest(http.MethodPost, "/api/v1/traces", nil)
		req.Header.Set("X-SpanBarn-Api-Key", readKey)
		rec := httptest.NewRecorder()
		newHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for non-GET read key, got %d", rec.Code)
		}
	})

	t.Run("ingest key forbidden", func(t *testing.T) {
		lookup.hash = auth.HashKey(ingestKey)
		lookup.rec = auth.APIKeyRecord{ID: 2, ProjectID: 7, Scope: "ingest"}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
		req.Header.Set("X-SpanBarn-Api-Key", ingestKey)
		rec := httptest.NewRecorder()
		newHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for ingest key, got %d", rec.Code)
		}
	})

	t.Run("invalid key unauthorized", func(t *testing.T) {
		lookup.hash = auth.HashKey(readKey)
		lookup.rec = auth.APIKeyRecord{ID: 1, ProjectID: 7, Scope: "read"}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
		req.Header.Set("X-SpanBarn-Api-Key", "totally-wrong")
		rec := httptest.NewRecorder()
		newHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 for invalid key, got %d", rec.Code)
		}
	})

	t.Run("session still works without key", func(t *testing.T) {
		var gotUser string
		handler := SessionOrReadKey(sm, authorizer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser = GetUsername(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
		req.AddCookie(&http.Cookie{Name: "session", Value: token})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for session, got %d", rec.Code)
		}
		if gotUser != "admin" {
			t.Errorf("expected username 'admin', got %q", gotUser)
		}
	})

	t.Run("no credentials unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
		rec := httptest.NewRecorder()
		newHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 with no creds, got %d", rec.Code)
		}
	})

	t.Run("nil authorizer falls back to session", func(t *testing.T) {
		handler := SessionOrReadKey(sm, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		// A key is ignored when no authorizer is configured; session required.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
		req.Header.Set("X-SpanBarn-Api-Key", readKey)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 (no session, key ignored), got %d", rec.Code)
		}
	})
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
