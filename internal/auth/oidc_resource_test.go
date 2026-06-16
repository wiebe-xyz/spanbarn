package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// oidcTestIssuer spins up a minimal OIDC issuer (discovery + JWKS) signing
// EdDSA tokens, mirroring IamBarn, so VerifyAccessToken can be exercised
// end-to-end without a live issuer.
type oidcTestIssuer struct {
	server *httptest.Server
	priv   ed25519.PrivateKey
	kid    string
}

func newOIDCTestIssuer(t *testing.T) *oidcTestIssuer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	it := &oidcTestIssuer{priv: priv, kid: "test-key"}

	mux := http.NewServeMux()
	it.server = httptest.NewServer(mux)
	issuer := it.server.URL

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/oauth2/authorize",
			"token_endpoint":                        issuer + "/oauth2/token",
			"jwks_uri":                              issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"EdDSA"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       pub,
			KeyID:     it.kid,
			Algorithm: "EdDSA",
			Use:       "sig",
		}}})
	})
	t.Cleanup(it.server.Close)
	return it
}

func (it *oidcTestIssuer) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: jose.JSONWebKey{Key: it.priv, KeyID: it.kid, Algorithm: "EdDSA"}},
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
	tok, err := obj.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return tok
}

func (it *oidcTestIssuer) client(aud ...string) *OIDCClient {
	return NewOIDCClient(OIDCConfig{
		Issuer:            it.server.URL,
		ClientID:          "spanbarn-web",
		ResourceAudiences: aud,
	})
}

func baseClaims(issuer string) map[string]any {
	return map[string]any{
		"iss":       issuer,
		"sub":       "user-123",
		"aud":       []string{"spanbarn-cli"},
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": "access_token",
		"roles":     []string{"operator"},
		"email":     "wiebe@wiebe.xyz",
	}
}

func TestVerifyAccessToken_Valid(t *testing.T) {
	it := newOIDCTestIssuer(t)
	oc := it.client("spanbarn-cli")
	tok := it.sign(t, baseClaims(it.server.URL))

	claims, err := oc.VerifyAccessToken(context.Background(), tok)
	if err != nil {
		t.Fatalf("expected valid token, got %v", err)
	}
	if claims.PreferredName() != "wiebe@wiebe.xyz" {
		t.Errorf("preferred name = %q", claims.PreferredName())
	}
	if !oc.Allowed(claims) {
		t.Errorf("operator role should be allowed")
	}
}

func TestVerifyAccessToken_WrongAudience(t *testing.T) {
	it := newOIDCTestIssuer(t)
	oc := it.client("spanbarn-cli") // only accepts spanbarn-cli
	c := baseClaims(it.server.URL)
	c["aud"] = []string{"some-other-app"}
	tok := it.sign(t, c)

	if _, err := oc.VerifyAccessToken(context.Background(), tok); err == nil {
		t.Fatal("expected audience rejection")
	}
}

func TestVerifyAccessToken_Expired(t *testing.T) {
	it := newOIDCTestIssuer(t)
	oc := it.client("spanbarn-cli")
	c := baseClaims(it.server.URL)
	c["exp"] = time.Now().Add(-time.Hour).Unix()
	tok := it.sign(t, c)

	if _, err := oc.VerifyAccessToken(context.Background(), tok); err == nil {
		t.Fatal("expected expiry rejection")
	}
}

func TestVerifyAccessToken_NotAccessToken(t *testing.T) {
	it := newOIDCTestIssuer(t)
	oc := it.client("spanbarn-cli")
	c := baseClaims(it.server.URL)
	c["token_use"] = "id_token"
	tok := it.sign(t, c)

	if _, err := oc.VerifyAccessToken(context.Background(), tok); err == nil {
		t.Fatal("expected non-access-token rejection")
	}
}

func TestVerifyAccessToken_ClientIDAudienceAccepted(t *testing.T) {
	it := newOIDCTestIssuer(t)
	oc := it.client() // no explicit resource audiences; ClientID implicit
	c := baseClaims(it.server.URL)
	c["aud"] = []string{"spanbarn-web"} // == ClientID
	tok := it.sign(t, c)

	if _, err := oc.VerifyAccessToken(context.Background(), tok); err != nil {
		t.Fatalf("ClientID audience should be accepted, got %v", err)
	}
}

func TestVerifyAccessToken_NotAllowedRole(t *testing.T) {
	it := newOIDCTestIssuer(t)
	oc := it.client("spanbarn-cli")
	c := baseClaims(it.server.URL)
	c["roles"] = []string{"member"}
	delete(c, "groups")
	tok := it.sign(t, c)

	claims, err := oc.VerifyAccessToken(context.Background(), tok)
	if err != nil {
		t.Fatalf("token should verify, got %v", err)
	}
	if oc.Allowed(claims) {
		t.Error("member role without group should not be allowed")
	}
}

func TestAudienceAllowed(t *testing.T) {
	cases := []struct {
		tok, allow []string
		want       bool
	}{
		{[]string{"a", "b"}, []string{"b"}, true},
		{[]string{"a"}, []string{"b", "c"}, false},
		{[]string{"a"}, []string{""}, false},
		{nil, []string{"a"}, false},
		{[]string{"a"}, nil, false},
	}
	for i, tc := range cases {
		if got := audienceAllowed(tc.tok, tc.allow); got != tc.want {
			t.Errorf("case %d: audienceAllowed(%v,%v)=%v want %v", i, tc.tok, tc.allow, got, tc.want)
		}
	}
}
