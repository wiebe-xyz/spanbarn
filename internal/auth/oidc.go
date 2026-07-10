// Package auth's OIDC adapter is used when SPANBARN_OIDC_* env vars are set.
// Local single-user login (SPANBARN_ADMIN_PASSWORD / _BCRYPT) remains the
// zero-config default — OIDC is an additional, opt-in flow.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	oidcv3 "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig is the static configuration for the OIDC login flow.
type OIDCConfig struct {
	Issuer        string // iambarn issuer URL, e.g. https://iam.wiebe.xyz
	ClientID      string
	ClientSecret  string
	RedirectURL   string // e.g. https://spanbarn.wiebe.xyz/api/v1/oidc/callback
	RequiredGroup string // group slug that grants access; bypass roles always win

	// ResourceAudiences lists the audiences accepted on IamBarn access-token
	// JWTs when SpanBarn acts as an OAuth2 resource server (the `sb` CLI's
	// device-code and client-credentials logins). An access token is accepted
	// only if one of its `aud` values is in this list. ClientID is always
	// included implicitly.
	ResourceAudiences []string

	// CLIClientID is the public IamBarn client the `sb` CLI uses for the
	// device-code flow. Advertised to the CLI via /api/v1/client-config.
	CLIClientID string

	// PostLogoutRedirectURI is where IamBarn returns the browser after an
	// RP-initiated /oauth2/end-session logout. Advertised to the SPA via
	// /api/v1/client-config so the hosted widgets can build the logout URL.
	PostLogoutRedirectURI string
}

// Enabled reports whether all four required fields are present.
func (c OIDCConfig) Enabled() bool {
	return c.Issuer != "" && c.ClientID != "" && c.ClientSecret != "" && c.RedirectURL != ""
}

// OIDCClient lazily discovers the issuer's OpenID configuration and exposes
// the few primitives the HTTP layer needs: an authorize URL, a code-exchange,
// and an ID-token verification that also enforces the access policy.
type OIDCClient struct {
	cfg     OIDCConfig
	timeout time.Duration

	mu             sync.Mutex
	provider       *oidcv3.Provider
	verifier       *oidcv3.IDTokenVerifier // ID tokens: aud must equal ClientID
	accessVerifier *oidcv3.IDTokenVerifier // access tokens: aud checked manually
	oauth          *oauth2.Config
}

// NewOIDCClient returns a client. Discovery is deferred to the first call so
// that an unreachable issuer at startup does not crash the process.
func NewOIDCClient(cfg OIDCConfig) *OIDCClient {
	if cfg.RequiredGroup == "" {
		cfg.RequiredGroup = "spanbarn-users"
	}
	return &OIDCClient{cfg: cfg, timeout: 10 * time.Second}
}

// Config returns the static configuration. Used to expose non-secret bits
// (login URL) via client-config.
func (c *OIDCClient) Config() OIDCConfig { return c.cfg }

// AuthorizeURL builds the URL the browser should be redirected to. The caller
// is responsible for storing state + nonce in short-lived cookies and matching
// them on callback.
func (c *OIDCClient) AuthorizeURL(state, nonce string) (string, error) {
	if err := c.ensureReady(context.Background()); err != nil {
		return "", err
	}
	return c.oauth.AuthCodeURL(state, oidcv3.Nonce(nonce)), nil
}

// ExchangeResult holds the parsed claims and the raw OIDC access token.
type ExchangeResult struct {
	Claims      OIDCClaims
	AccessToken string
}

// ExchangeFull swaps an authorization code for tokens, verifies the ID token,
// and returns both the parsed claims and the raw access token in one call.
func (c *OIDCClient) ExchangeFull(ctx context.Context, code, nonce string) (ExchangeResult, error) {
	if err := c.ensureReady(ctx); err != nil {
		return ExchangeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	tok, err := c.oauth.Exchange(ctx, code)
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("oidc: token exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return ExchangeResult{}, errors.New("oidc: token response missing id_token")
	}
	idToken, err := c.verifier.Verify(ctx, rawID)
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("oidc: verify id_token: %w", err)
	}
	if idToken.Nonce != nonce {
		return ExchangeResult{}, errors.New("oidc: nonce mismatch")
	}
	var claims OIDCClaims
	if err := idToken.Claims(&claims); err != nil {
		return ExchangeResult{}, fmt.Errorf("oidc: decode claims: %w", err)
	}
	return ExchangeResult{Claims: claims, AccessToken: tok.AccessToken}, nil
}

// Exchange swaps an authorization code for tokens and verifies the ID token's
// signature + audience + nonce. Returns the parsed ID-token claims on success.
func (c *OIDCClient) Exchange(ctx context.Context, code, nonce string) (OIDCClaims, error) {
	r, err := c.ExchangeFull(ctx, code, nonce)
	return r.Claims, err
}

// VerifyAccessToken validates an IamBarn access-token JWT (presented by the
// `sb` CLI as a Bearer token) and returns its claims. It checks the signature
// and issuer/expiry via the issuer's JWKS, requires token_use=="access_token",
// and requires one of the token's audiences to be in ResourceAudiences (or to
// equal the configured ClientID). Authorization (role/group) is left to the
// caller via Allowed.
func (c *OIDCClient) VerifyAccessToken(ctx context.Context, raw string) (OIDCClaims, error) {
	if err := c.ensureReady(ctx); err != nil {
		return OIDCClaims{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	tok, err := c.accessVerifier.Verify(ctx, raw)
	if err != nil {
		return OIDCClaims{}, fmt.Errorf("oidc: verify access token: %w", err)
	}

	allowed := append([]string{c.cfg.ClientID}, c.cfg.ResourceAudiences...)
	if !audienceAllowed(tok.Audience, allowed) {
		return OIDCClaims{}, fmt.Errorf("oidc: token audience %v not accepted", tok.Audience)
	}

	var claims OIDCClaims
	if err := tok.Claims(&claims); err != nil {
		return OIDCClaims{}, fmt.Errorf("oidc: decode access-token claims: %w", err)
	}
	if claims.TokenUse != "access_token" {
		return OIDCClaims{}, errors.New("oidc: not an access token")
	}
	return claims, nil
}

// audienceAllowed reports whether any token audience appears in the allowlist
// (ignoring empty entries).
func audienceAllowed(tokenAud, allowed []string) bool {
	for _, a := range allowed {
		if a == "" {
			continue
		}
		for _, t := range tokenAud {
			if t == a {
				return true
			}
		}
	}
	return false
}

// Allowed returns true if the claims grant access to this barn.
// Owner/organization_admin/operator roles bypass the group check.
func (c *OIDCClient) Allowed(claims OIDCClaims) bool {
	for _, role := range claims.Roles {
		switch role {
		case "owner", "organization_admin", "operator":
			return true
		}
	}
	for _, g := range claims.Groups {
		if g == c.cfg.RequiredGroup {
			return true
		}
	}
	return false
}

// ensureReady performs the one-time discovery + provider wiring. Safe for
// concurrent callers.
func (c *OIDCClient) ensureReady(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	prov, err := oidcv3.NewProvider(ctx, strings.TrimRight(c.cfg.Issuer, "/"))
	if err != nil {
		return fmt.Errorf("oidc: discover issuer %q: %w", c.cfg.Issuer, err)
	}
	c.provider = prov
	c.verifier = prov.Verifier(&oidcv3.Config{ClientID: c.cfg.ClientID})
	// Access tokens carry the requesting client's id as aud (not SpanBarn's
	// OIDC client id), so skip the built-in aud check and enforce our own
	// ResourceAudiences allowlist in VerifyAccessToken.
	c.accessVerifier = prov.Verifier(&oidcv3.Config{SkipClientIDCheck: true})
	c.oauth = &oauth2.Config{
		ClientID:     c.cfg.ClientID,
		ClientSecret: c.cfg.ClientSecret,
		Endpoint:     prov.Endpoint(),
		RedirectURL:  c.cfg.RedirectURL,
		Scopes:       []string{oidcv3.ScopeOpenID, "profile", "email"},
	}
	return nil
}

// OIDCClaims is the subset of ID-token / access-token claims this barn cares
// about.
type OIDCClaims struct {
	Subject           string   `json:"sub"`
	Email             string   `json:"email"`
	PreferredUsername string   `json:"preferred_username"`
	Name              string   `json:"name"`
	Groups            []string `json:"groups"`
	Roles             []string `json:"roles"`
	TokenUse          string   `json:"token_use"` // "access_token" for resource-server validation
	ClientName        string   `json:"client_name"`
}

// PreferredName returns the best human-readable identifier from the claims.
func (c OIDCClaims) PreferredName() string {
	for _, v := range []string{c.PreferredUsername, c.Email, c.Name, c.ClientName, c.Subject} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
