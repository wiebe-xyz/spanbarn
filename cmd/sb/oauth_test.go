package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeIssuer serves SpanBarn client-config + OIDC discovery + token/device
// endpoints on one mux, enough to drive the CLI's OAuth flows. The access
// tokens are opaque strings (the CLI doesn't validate them).
func fakeIssuer(t *testing.T) (*httptest.Server, *fakeState) {
	t.Helper()
	st := &fakeState{}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/api/v1/client-config", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"oidc": map[string]any{"enabled": true, "issuer": srv.URL, "cli_client_id": "spanbarn-cli"},
		})
	})
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                        srv.URL,
			"token_endpoint":                srv.URL + "/oauth2/token",
			"device_authorization_endpoint": srv.URL + "/oauth2/device_authorization",
			"authorization_endpoint":        srv.URL + "/oauth2/authorize",
		})
	})
	mux.HandleFunc("/oauth2/device_authorization", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dev-123", "user_code": "WXYZ-1234",
			"verification_uri": srv.URL + "/device", "expires_in": 60, "interval": 1,
		})
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		st.lastGrant = r.Form.Get("grant_type")
		switch st.lastGrant {
		case "client_credentials":
			u, p, ok := r.BasicAuth()
			st.basicUser, st.basicOK = u, ok
			_ = p
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "m2m-token", "token_type": "Bearer", "expires_in": 3600})
		case "urn:ietf:params:oauth:grant-type:device_code":
			st.devicePolls++
			if st.devicePolls < 2 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "device-token", "refresh_token": "refresh-1", "expires_in": 3600})
		case "refresh_token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "device-token-2", "refresh_token": "refresh-2", "expires_in": 3600})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unsupported_grant_type"})
		}
	})
	return srv, st
}

type fakeState struct {
	lastGrant   string
	basicUser   string
	basicOK     bool
	devicePolls int
}

func TestClientCredentialsLogin(t *testing.T) {
	srv, st := fakeIssuer(t)
	cfg := Config{URL: srv.URL}
	if err := clientCredentialsLogin(&cfg, "", "imc_x", "secret", "openid"); err != nil {
		t.Fatalf("m2m login: %v", err)
	}
	if cfg.AccessToken != "m2m-token" || cfg.AuthType != "oidc-m2m" {
		t.Errorf("unexpected cfg: %+v", cfg)
	}
	if !st.basicOK || st.basicUser == "" {
		t.Error("expected client_secret_basic auth")
	}
	if cfg.TokenExpiry == 0 {
		t.Error("expected token expiry to be set")
	}
}

func TestDeviceLogin(t *testing.T) {
	srv, st := fakeIssuer(t)
	cfg := Config{URL: srv.URL}
	if err := deviceLogin(&cfg); err != nil {
		t.Fatalf("device login: %v", err)
	}
	if cfg.AccessToken != "device-token" || cfg.RefreshToken != "refresh-1" || cfg.AuthType != "oidc-device" {
		t.Errorf("unexpected cfg: %+v", cfg)
	}
	if st.devicePolls < 2 {
		t.Errorf("expected polling through authorization_pending, got %d polls", st.devicePolls)
	}
	if cfg.OIDCClientID != "spanbarn-cli" {
		t.Errorf("expected cli client id, got %q", cfg.OIDCClientID)
	}
}

func TestRefreshOIDCToken_Device(t *testing.T) {
	srv, _ := fakeIssuer(t)
	cfg := Config{
		URL: srv.URL, AuthType: "oidc-device", OIDCIssuer: srv.URL,
		OIDCClientID: "spanbarn-cli", RefreshToken: "refresh-1", AccessToken: "old",
	}
	if err := refreshOIDCToken(&cfg); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if cfg.AccessToken != "device-token-2" || cfg.RefreshToken != "refresh-2" {
		t.Errorf("expected rotated tokens, got %+v", cfg)
	}
}

func TestDeviceLogin_OIDCDisabled(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/api/v1/client-config", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{}) // no oidc block
	})
	cfg := Config{URL: srv.URL}
	if err := deviceLogin(&cfg); err == nil {
		t.Fatal("expected error when OIDC is not enabled")
	}
}
