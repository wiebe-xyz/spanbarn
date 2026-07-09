package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

// newTestOIDC starts a minimal EdDSA OIDC issuer and returns a wired
// OIDCClient plus a token signer, mirroring IamBarn for resource-server tests.
func newTestOIDC(t *testing.T, audiences ...string) (*auth.OIDCClient, func(map[string]any) string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	const kid = "k1"
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	issuer := srv.URL
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"EdDSA"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: pub, KeyID: kid, Algorithm: "EdDSA", Use: "sig",
		}}})
	})

	oc := auth.NewOIDCClient(auth.OIDCConfig{Issuer: issuer, ClientID: "spanbarn-web", ResourceAudiences: audiences})

	sign := func(claims map[string]any) string {
		signer, err := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.EdDSA, Key: jose.JSONWebKey{Key: priv, KeyID: kid, Algorithm: "EdDSA"}},
			(&jose.SignerOptions{}).WithType("JWT"))
		if err != nil {
			t.Fatalf("signer: %v", err)
		}
		payload, _ := json.Marshal(claims)
		obj, err := signer.Sign(payload)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		tok, _ := obj.CompactSerialize()
		return tok
	}
	return oc, sign, issuer
}

func TestSessionOrReadKey_IamBarnJWT(t *testing.T) {
	sm := auth.NewSessionManager("test-secret", 3600)
	oc, sign, issuer := newTestOIDC(t, "spanbarn-cli")
	oidcFn := func() *auth.OIDCClient { return oc }

	var gotUser string
	handler := SessionOrReadKey(sm, nil, oidcFn)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = GetUsername(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	base := func() map[string]any {
		return map[string]any{
			"iss": issuer, "sub": "u1", "aud": []string{"spanbarn-cli"},
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
			"token_use": "access_token", "roles": []string{"operator"},
			"preferred_username": "wiebe",
		}
	}

	t.Run("valid token reaches handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
		req.Header.Set("Authorization", "Bearer "+sign(base()))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if gotUser != "wiebe" {
			t.Errorf("username = %q", gotUser)
		}
	})

	t.Run("write method rejected as read-only → 403", func(t *testing.T) {
		for _, m := range []string{http.MethodDelete, http.MethodPost, http.MethodPut} {
			req := httptest.NewRequest(m, "/api/v1/projects/1", nil)
			req.Header.Set("Authorization", "Bearer "+sign(base()))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s: expected 403 (read-only), got %d", m, rec.Code)
			}
		}
	})

	t.Run("disallowed role → 403", func(t *testing.T) {
		c := base()
		c["roles"] = []string{"member"}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
		req.Header.Set("Authorization", "Bearer "+sign(c))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", rec.Code)
		}
	})

	t.Run("wrong audience → 401", func(t *testing.T) {
		c := base()
		c["aud"] = []string{"nope"}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
		req.Header.Set("Authorization", "Bearer "+sign(c))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("nil oidc falls back to session (JWT rejected as session)", func(t *testing.T) {
		h := SessionOrReadKey(sm, nil, func() *auth.OIDCClient { return nil })(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
		req.Header.Set("Authorization", "Bearer "+sign(base()))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 (no oidc, JWT not a valid session), got %d", rec.Code)
		}
	})
}
