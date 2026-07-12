package auth

import (
	"context"
	"testing"
	"time"
)

// logoutTokenClaims returns a valid back-channel logout token claim set for
// the given issuer; tests mutate it to exercise each rejection path.
func logoutTokenClaims(issuer string) map[string]any {
	return map[string]any{
		"iss": issuer,
		"aud": "spanbarn-web",
		"sub": "user-123",
		"sid": "sess-abc",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(2 * time.Minute).Unix(),
		"jti": "jti-1",
		"events": map[string]any{
			"http://schemas.openid.net/event/backchannel-logout": map[string]any{},
		},
	}
}

func logoutTestClient(it *oidcTestIssuer) *OIDCClient {
	return NewOIDCClient(OIDCConfig{
		Issuer:       it.server.URL,
		ClientID:     "spanbarn-web",
		ClientSecret: "sek",
		RedirectURL:  "https://spanbarn.example.com/api/v1/oidc/callback",
	})
}

func TestVerifyLogoutToken_Valid(t *testing.T) {
	it := newOIDCTestIssuer(t)
	oc := logoutTestClient(it)

	got, err := oc.VerifyLogoutToken(context.Background(), it.sign(t, logoutTokenClaims(it.server.URL)))
	if err != nil {
		t.Fatalf("VerifyLogoutToken: %v", err)
	}
	if got.Subject != "user-123" || got.SessionID != "sess-abc" {
		t.Fatalf("claims = %+v", got)
	}
}

func TestVerifyLogoutToken_SubOnly(t *testing.T) {
	it := newOIDCTestIssuer(t)
	oc := logoutTestClient(it)

	claims := logoutTokenClaims(it.server.URL)
	delete(claims, "sid")
	got, err := oc.VerifyLogoutToken(context.Background(), it.sign(t, claims))
	if err != nil {
		t.Fatalf("VerifyLogoutToken: %v", err)
	}
	if got.Subject != "user-123" || got.SessionID != "" {
		t.Fatalf("claims = %+v", got)
	}
}

func TestVerifyLogoutToken_Rejections(t *testing.T) {
	it := newOIDCTestIssuer(t)
	oc := logoutTestClient(it)
	issuer := it.server.URL

	cases := map[string]func(map[string]any){
		// The spec REQUIRES rejecting a logout token carrying a nonce — that
		// is what stops an ID token being replayed as a logout token.
		"nonce present": func(c map[string]any) { c["nonce"] = "n-1" },
		"missing events": func(c map[string]any) {
			delete(c, "events")
		},
		"wrong event member": func(c map[string]any) {
			c["events"] = map[string]any{"http://example.com/other": map[string]any{}}
		},
		"stale iat": func(c map[string]any) {
			c["iat"] = time.Now().Add(-10 * time.Minute).Unix()
		},
		"future iat": func(c map[string]any) {
			c["iat"] = time.Now().Add(10 * time.Minute).Unix()
		},
		"neither sub nor sid": func(c map[string]any) {
			delete(c, "sub")
			delete(c, "sid")
		},
		"wrong audience": func(c map[string]any) { c["aud"] = "other-client" },
		"wrong issuer":   func(c map[string]any) { c["iss"] = "https://evil.example" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			claims := logoutTokenClaims(issuer)
			mutate(claims)
			if _, err := oc.VerifyLogoutToken(context.Background(), it.sign(t, claims)); err == nil {
				t.Fatal("expected verification to fail")
			}
		})
	}
}

func TestVerifyLogoutToken_BadSignature(t *testing.T) {
	// A token signed by a DIFFERENT issuer's key must be rejected even when
	// its claims are perfect.
	it := newOIDCTestIssuer(t)
	other := newOIDCTestIssuer(t)
	oc := logoutTestClient(it)

	if _, err := oc.VerifyLogoutToken(context.Background(), other.sign(t, logoutTokenClaims(it.server.URL))); err == nil {
		t.Fatal("expected signature verification to fail")
	}
}
