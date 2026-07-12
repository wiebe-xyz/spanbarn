package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// newTestRepo returns a repository over a migrated in-memory SQLite DB.
func newTestRepo(t *testing.T) *repository.Repository {
	t.Helper()
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return repository.NewRepository(db.DB)
}

// newTestSessions returns a SessionService over a fresh in-memory repository.
func newTestSessions(t *testing.T) (*SessionService, *repository.Repository) {
	t.Helper()
	repo := newTestRepo(t)
	return NewSessionService(repo, 3600, 3600, nil), repo
}

// mintSession creates a local session row and returns its opaque token.
func mintSession(t *testing.T, svc *SessionService, username string) string {
	t.Helper()
	token, _, err := svc.Create(username, "local", nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return token
}

// fakeIdP is an in-process iambarn stand-in: discovery, an Ed25519 JWKS, and
// a scriptable token endpoint that serves both the authorization_code and
// refresh_token grants with signed id_tokens. It drives the full session
// lifecycle — login exchange, expiry → refresh, invalid_grant, 5xx outage —
// without a live issuer.
type fakeIdP struct {
	srv  *httptest.Server
	priv ed25519.PrivateKey
	kid  string

	mu sync.Mutex
	// scripted token endpoint behaviour
	tokenStatus   int // 0 → 200; 400 → invalid_grant; 500 → server_error
	accessToken   string
	refreshToken  string
	expiresIn     int
	idClaims      map[string]any // extra claims merged into the id_token
	tokenCalls    int
	lastTokenForm map[string][]string
	// meAccess is the access token /api/v1/me currently accepts. A
	// successful token-endpoint call rotates it to accessToken.
	meAccess string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	f := &fakeIdP{
		priv:         priv,
		kid:          "k1",
		accessToken:  "at-1",
		refreshToken: "rt-1",
		expiresIn:    900,
	}

	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	issuer := f.srv.URL

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/oauth2/authorize",
			"token_endpoint":                        issuer + "/oauth2/token",
			"jwks_uri":                              issuer + "/jwks",
			"revocation_endpoint":                   issuer + "/oauth2/revoke",
			"end_session_endpoint":                  issuer + "/oauth2/end-session",
			"id_token_signing_alg_values_supported": []string{"EdDSA"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: pub, KeyID: f.kid, Algorithm: "EdDSA", Use: "sig",
		}}})
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.tokenCalls++
		f.lastTokenForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		switch f.tokenStatus {
		case http.StatusBadRequest:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		case http.StatusInternalServerError:
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "server_error"})
			return
		}
		claims := map[string]any{
			"iss":   issuer,
			"aud":   "spanbarn-web",
			"sub":   "sub-1",
			"sid":   "sid-1",
			"iat":   time.Now().Unix(),
			"exp":   time.Now().Add(time.Hour).Unix(),
			"name":  "Wiebe",
			"email": "wiebe@wiebe.xyz",
			"roles": []string{"owner"},
		}
		if nonce := r.PostForm.Get("nonce"); nonce != "" {
			claims["nonce"] = nonce
		}
		for k, v := range f.idClaims {
			claims[k] = v
		}
		f.meAccess = f.accessToken
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  f.accessToken,
			"refresh_token": f.refreshToken,
			"id_token":      f.signLocked(t, claims),
			"token_type":    "Bearer",
			"expires_in":    f.expiresIn,
		})
	})
	mux.HandleFunc("/oauth2/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.lastTokenForm = r.PostForm
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	// Widget-style upstream API used by the IAM-proxy tests: accepts exactly
	// the access token the IdP currently considers valid (meAccess).
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		want := f.meAccess
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+want || want == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"username": "wiebe"})
	})
	return f
}

// signLocked signs claims with the IdP key; callers must hold f.mu (or use
// sign) — jose signers are cheap enough to build per call.
func (f *fakeIdP) signLocked(t *testing.T, claims map[string]any) string {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: jose.JSONWebKey{Key: f.priv, KeyID: f.kid, Algorithm: "EdDSA"}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	payload, _ := json.Marshal(claims)
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := obj.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

// sign signs a claim set (e.g. a logout token) with the IdP key.
func (f *fakeIdP) sign(t *testing.T, claims map[string]any) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.signLocked(t, claims)
}

// script updates the token endpoint's behaviour for the next calls.
func (f *fakeIdP) script(status int, accessToken, refreshToken string, idClaims map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenStatus = status
	if accessToken != "" {
		f.accessToken = accessToken
	}
	if refreshToken != "" {
		f.refreshToken = refreshToken
	}
	f.idClaims = idClaims
}

func (f *fakeIdP) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls
}

func (f *fakeIdP) client() *auth.OIDCClient {
	return auth.NewOIDCClient(auth.OIDCConfig{
		Issuer:                f.srv.URL,
		ClientID:              "spanbarn-web",
		ClientSecret:          "sek",
		RedirectURL:           "https://spanbarn.example.com/api/v1/oidc/callback",
		PostLogoutRedirectURI: "https://spanbarn.example.com/api/v1/oidc/logout-complete",
	})
}

// newOIDCSessionRow creates an OIDC session directly in the store (as the
// callback would) and returns its opaque token. accessExpiresAt controls when
// the middleware must refresh.
func newOIDCSessionRow(t *testing.T, svc *SessionService, accessExpiresAt time.Time, refreshToken string) string {
	t.Helper()
	token, _, err := svc.Create("Wiebe", "oidc", &OIDCSessionData{
		Claims: auth.OIDCClaims{
			Subject:   "sub-1",
			SessionID: "sid-1",
			Name:      "Wiebe",
			Email:     "wiebe@wiebe.xyz",
			Roles:     []string{"owner"},
		},
		IDToken:         "idtok-login",
		AccessToken:     "at-login",
		RefreshToken:    refreshToken,
		AccessExpiresAt: accessExpiresAt,
	})
	if err != nil {
		t.Fatalf("create oidc session: %v", err)
	}
	return token
}
