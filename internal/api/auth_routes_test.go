package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postLogin(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "correct-pass"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServerServesLoginAndLogoutWithLogin(t *testing.T) {
	userAuth, sm := setupLoginTest(t)
	s := NewServerWithQuery(ServerConfig{}, nil, nil, sm, slog.Default(), WithLogin(userAuth, 100, 100, 100))

	rec := postLogin(t, s.Handler())
	if rec.Code != http.StatusOK {
		t.Fatalf("login: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("login set no session cookie")
	}

	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, httptest.NewRequest(http.MethodPost, "/api/v1/logout", nil))
	if out.Code != http.StatusOK {
		t.Fatalf("logout: want 200, got %d", out.Code)
	}

	health := httptest.NewRecorder()
	s.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health through the auth mux: want 200, got %d", health.Code)
	}
}

func TestServerSkipsLoginWithoutAuthenticatorOrSessions(t *testing.T) {
	userAuth, sm := setupLoginTest(t)
	cases := map[string]*Server{
		"no WithLogin": NewServerWithQuery(ServerConfig{}, nil, nil, sm, slog.Default()),
		"no sessions":  NewServerWithQuery(ServerConfig{}, nil, nil, nil, slog.Default(), WithLogin(userAuth, 100, 100, 100)),
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if s.authMux != nil {
				t.Fatal("auth routes registered")
			}
			if rec := postLogin(t, s.Handler()); rec.Code == http.StatusOK {
				t.Fatalf("login answered 200 without its dependencies")
			}
		})
	}
}

func TestLoginRateLimitedPerIP(t *testing.T) {
	userAuth, sm := setupLoginTest(t)
	s := NewServerWithQuery(ServerConfig{}, nil, nil, sm, slog.Default(), WithLogin(userAuth, 1, 100, 100))
	var last int
	for i := 0; i < 5; i++ {
		last = postLogin(t, s.Handler()).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("want 429 after the limiter burst, got %d", last)
	}
}

func TestWriterProbes(t *testing.T) {
	var p WriterProbes
	mux := http.NewServeMux()
	p.Register(mux)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	health := get("/api/v1/health")
	if health.Code != http.StatusOK || health.Body.String() != `{"status":"ok","mode":"writer"}` {
		t.Fatalf("health: %d %q", health.Code, health.Body.String())
	}
	if ct := health.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("health content type %q", ct)
	}
	if rec := get("/api/v1/ready"); rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "not ready\n" {
		t.Fatalf("ready before SetReady: %d %q", rec.Code, rec.Body.String())
	}
	p.SetReady(true)
	if rec := get("/api/v1/ready"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("ready after SetReady: %d %q", rec.Code, rec.Body.String())
	}
	p.SetReady(false)
	if rec := get("/api/v1/ready"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready after SetReady(false): %d", rec.Code)
	}
}
