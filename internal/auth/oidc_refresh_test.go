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

	"golang.org/x/oauth2"
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

	raw, err := oc.AuthorizeURL("state1", "nonce1", oauth2.GenerateVerifier())
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

// TestAuthorizeURLCarriesPKCEChallenge: PKCE is sent even though SpanBarn is
// a confidential client — the S256 challenge must appear on the authorize URL
// so the later code exchange is bound to this browser flow.
func TestAuthorizeURLCarriesPKCEChallenge(t *testing.T) {
	srv := newRefreshTestIssuer(t, nil)
	oc := testOIDCClient(srv.URL)

	verifier := oauth2.GenerateVerifier()
	raw, err := oc.AuthorizeURL("state1", "nonce1", verifier)
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	if got := u.Query().Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	if got := u.Query().Get("code_challenge"); got == "" || got == verifier {
		t.Errorf("code_challenge = %q, want a non-empty S256 hash of the verifier", got)
	}
}

func TestRevokeRefreshToken(t *testing.T) {
	var gotForm url.Values
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
			"revocation_endpoint":                   srv.URL + "/oauth2/revoke",
			"id_token_signing_alg_values_supported": []string{"EdDSA"},
		})
	})
	mux.HandleFunc("/oauth2/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		gotForm = r.PostForm
		w.WriteHeader(http.StatusOK)
	})

	oc := testOIDCClient(srv.URL)
	if err := oc.RevokeRefreshToken(context.Background(), "rt-dead"); err != nil {
		t.Fatalf("RevokeRefreshToken: %v", err)
	}
	if gotForm.Get("token") != "rt-dead" {
		t.Errorf("token = %q, want rt-dead", gotForm.Get("token"))
	}
	if gotForm.Get("token_type_hint") != "refresh_token" {
		t.Errorf("token_type_hint = %q", gotForm.Get("token_type_hint"))
	}
	if gotForm.Get("client_id") != "spanbarn-web" || gotForm.Get("client_secret") != "sek" {
		t.Error("revocation must be client-authenticated")
	}

	// Empty token is a silent no-op — nothing to revoke.
	gotForm = nil
	if err := oc.RevokeRefreshToken(context.Background(), ""); err != nil {
		t.Fatalf("RevokeRefreshToken(\"\"): %v", err)
	}
	if gotForm != nil {
		t.Error("empty refresh token must not hit the revocation endpoint")
	}
}

func TestEndSessionURLFallsBackToConventionalPath(t *testing.T) {
	// The minimal discovery document has no end_session_endpoint, so the
	// client must fall back to {issuer}/oauth2/end-session.
	srv := newRefreshTestIssuer(t, nil)
	oc := NewOIDCClient(OIDCConfig{
		Issuer:                srv.URL,
		ClientID:              "spanbarn-web",
		ClientSecret:          "sek",
		RedirectURL:           "https://spanbarn.example.com/api/v1/oidc/callback",
		PostLogoutRedirectURI: "https://spanbarn.example.com/api/v1/oidc/logout-complete",
	})

	raw, err := oc.EndSessionURL("the-id-token")
	if err != nil {
		t.Fatalf("EndSessionURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Path != "/oauth2/end-session" {
		t.Errorf("path = %q, want /oauth2/end-session", u.Path)
	}
	q := u.Query()
	if q.Get("id_token_hint") != "the-id-token" {
		t.Errorf("id_token_hint = %q", q.Get("id_token_hint"))
	}
	if q.Get("client_id") != "spanbarn-web" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("post_logout_redirect_uri") != "https://spanbarn.example.com/api/v1/oidc/logout-complete" {
		t.Errorf("post_logout_redirect_uri = %q", q.Get("post_logout_redirect_uri"))
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
