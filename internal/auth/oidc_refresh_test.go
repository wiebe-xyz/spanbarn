package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newRefreshTestIssuer spins up a minimal OIDC issuer (discovery + token
// endpoint) so AuthorizeURL and Refresh can be exercised without a live
// iambarn. tokenHandler serves POST /oauth2/token; pass nil if the test never
// reaches the token endpoint.
func newRefreshTestIssuer(t *testing.T, tokenHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/oauth2/authorize",
			"token_endpoint":                        srv.URL + "/oauth2/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"EdDSA"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})
	if tokenHandler != nil {
		mux.HandleFunc("/oauth2/token", tokenHandler)
	}
	return srv
}

func testOIDCClient(issuer string) *OIDCClient {
	return NewOIDCClient(OIDCConfig{
		Issuer:       issuer,
		ClientID:     "spanbarn-web",
		ClientSecret: "sek",
		RedirectURL:  "https://spanbarn.example.com/api/v1/oidc/callback",
	})
}

func TestAuthorizeURLRequestsOfflineAccessScope(t *testing.T) {
	srv := newRefreshTestIssuer(t, nil)
	oc := testOIDCClient(srv.URL)

	raw, err := oc.AuthorizeURL("state1", "nonce1")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	scopes := strings.Fields(u.Query().Get("scope"))
	want := map[string]bool{"openid": false, "profile": false, "email": false, "offline_access": false}
	for _, s := range scopes {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for scope, got := range want {
		if !got {
			t.Errorf("authorize URL scope %q missing from %q", scope, u.Query().Get("scope"))
		}
	}
}

func TestOIDCClientRefreshSuccess(t *testing.T) {
	var gotForm url.Values
	srv := newRefreshTestIssuer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    900,
		})
	})
	oc := testOIDCClient(srv.URL)

	result, err := oc.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result.AccessToken != "new-access" {
		t.Errorf("AccessToken = %q, want new-access", result.AccessToken)
	}
	if result.RefreshToken != "new-refresh" {
		t.Errorf("RefreshToken = %q, want new-refresh (rotation)", result.RefreshToken)
	}
	if result.ExpiresAt.Before(time.Now().Add(14 * time.Minute)) {
		t.Errorf("ExpiresAt = %v, want ~15 minutes out", result.ExpiresAt)
	}
	if gotForm.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", gotForm.Get("grant_type"))
	}
	if gotForm.Get("refresh_token") != "old-refresh" {
		t.Errorf("refresh_token sent = %q, want old-refresh", gotForm.Get("refresh_token"))
	}
}

func TestOIDCClientRefreshInvalidGrant(t *testing.T) {
	srv := newRefreshTestIssuer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "invalid_grant",
			"error_description": "refresh token revoked or already used",
		})
	})
	oc := testOIDCClient(srv.URL)

	_, err := oc.Refresh(context.Background(), "dead-refresh")
	if !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("Refresh error = %v, want ErrRefreshInvalid", err)
	}
}

// TestOIDCClientRefreshMissingRotatedToken documents a golang.org/x/oauth2
// behavior we rely on: if a token response omits refresh_token, the library
// falls back to the refresh_token that was sent in the request instead of
// leaving it empty (its accommodation for non-rotating providers). IamBarn
// always rotates in practice, so this path is defensive, not expected.
func TestOIDCClientRefreshMissingRotatedToken(t *testing.T) {
	srv := newRefreshTestIssuer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access",
			"token_type":   "Bearer",
			"expires_in":   900,
		})
	})
	oc := testOIDCClient(srv.URL)

	result, err := oc.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result.RefreshToken != "old-refresh" {
		t.Errorf("RefreshToken = %q, want the fallback to the sent old-refresh", result.RefreshToken)
	}
}

func TestOIDCClientRefreshServerError(t *testing.T) {
	srv := newRefreshTestIssuer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server_error"}`))
	})
	oc := testOIDCClient(srv.URL)

	_, err := oc.Refresh(context.Background(), "old-refresh")
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
	if errors.Is(err, ErrRefreshInvalid) {
		t.Error("a transient server_error must not be classified as ErrRefreshInvalid — the caller must not treat it as a dead token")
	}
}
