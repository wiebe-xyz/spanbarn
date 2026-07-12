// Package auth's OIDC adapter is used when SPANBARN_OIDC_* env vars are set.
// Local single-user login (SPANBARN_ADMIN_PASSWORD / _BCRYPT) remains the
// zero-config default — OIDC is an additional, opt-in flow.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	// Optional endpoints from discovery, with conventional iambarn fallbacks
	// when the discovery document omits them.
	revocationEndpoint string
	endSessionEndpoint string
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
// is responsible for storing state + nonce (and the PKCE verifier) in
// short-lived cookies and matching them on callback. verifier is the PKCE
// code_verifier from oauth2.GenerateVerifier; its S256 challenge is sent with
// the authorization request even though SpanBarn is a confidential client
// (PKCE-everywhere hardens against code injection at zero cost).
func (c *OIDCClient) AuthorizeURL(state, nonce, verifier string) (string, error) {
	if err := c.ensureReady(context.Background()); err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oidcv3.Nonce(nonce)}
	if verifier != "" {
		opts = append(opts, oauth2.S256ChallengeOption(verifier))
	}
	return c.oauth.AuthCodeURL(state, opts...), nil
}

// ExchangeResult holds the parsed claims and the raw OIDC tokens.
type ExchangeResult struct {
	Claims       OIDCClaims
	IDToken      string // raw id_token; kept for id_token_hint at logout
	AccessToken  string
	RefreshToken string    // empty if the client/grant did not include offline_access
	ExpiresAt    time.Time // zero if the token response omitted expires_in
}

// ExchangeFull swaps an authorization code for tokens, verifies the ID token,
// and returns both the parsed claims (including the IdP session id `sid`) and
// the raw tokens in one call. verifier is the PKCE code_verifier that produced
// the challenge sent at authorize time; pass "" only for flows started before
// PKCE was introduced.
func (c *OIDCClient) ExchangeFull(ctx context.Context, code, nonce, verifier string) (ExchangeResult, error) {
	if err := c.ensureReady(ctx); err != nil {
		return ExchangeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var opts []oauth2.AuthCodeOption
	if verifier != "" {
		opts = append(opts, oauth2.VerifierOption(verifier))
	}
	tok, err := c.oauth.Exchange(ctx, code, opts...)
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
	return ExchangeResult{
		Claims:       claims,
		IDToken:      rawID,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.Expiry,
	}, nil
}

// ErrRefreshInvalid indicates iambarn rejected the refresh_token outright
// (invalid_grant: revoked, expired, already-rotated/replayed, or the user was
// suspended). The caller must not retry the same token — it is dead — and
// should fall back to a full interactive login.
var ErrRefreshInvalid = errors.New("oidc: refresh token invalid")

// RefreshedTokens holds the renewed access/refresh token pair from a
// refresh_token grant. Iambarn rotates the refresh token on every use, so
// RefreshToken here always replaces whatever was previously stored.
type RefreshedTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	// IDToken and Claims are set when the token response included a fresh
	// id_token (iambarn does on refresh). Claims lets the caller re-snapshot
	// groups/roles so central role changes propagate on the next refresh
	// instead of the next login.
	IDToken string
	Claims  *OIDCClaims
}

// Refresh exchanges a refresh_token for a new access/refresh token pair.
// It authenticates as the client using the same style already established
// for the authorization_code exchange (c.oauth carries ClientID/ClientSecret
// and golang.org/x/oauth2 picks and caches the auth style per token
// endpoint), so no separate client-auth logic is needed here.
//
// Refresh tokens are single-use: iambarn invalidates the one sent here the
// moment it issues the replacement. Callers MUST NOT invoke Refresh
// concurrently with the same refreshToken — a second call with an
// already-rotated token is treated as a replay and revokes the whole token
// family. Callers are responsible for serializing refreshes per session
// (e.g. via singleflight).
func (c *OIDCClient) Refresh(ctx context.Context, refreshToken string) (RefreshedTokens, error) {
	if err := c.ensureReady(ctx); err != nil {
		return RefreshedTokens{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	tok, err := c.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken}).Token()
	if err != nil {
		var retrieveErr *oauth2.RetrieveError
		if errors.As(err, &retrieveErr) && retrieveErr.ErrorCode == "invalid_grant" {
			return RefreshedTokens{}, ErrRefreshInvalid
		}
		return RefreshedTokens{}, fmt.Errorf("oidc: refresh token: %w", err)
	}
	// golang.org/x/oauth2 falls back to the refresh_token we sent if the
	// response omits one (its accommodation for non-rotating providers), so
	// tok.RefreshToken is never empty here on a successful response —
	// iambarn always rotates, so in practice this is always the new token.
	refreshed := RefreshedTokens{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.Expiry,
	}
	// A refresh response may carry a fresh id_token with up-to-date claims.
	// Verify it like the login-time one (signature/issuer/audience), except
	// the nonce — refresh grants have no browser round-trip to bind one to.
	if rawID, ok := tok.Extra("id_token").(string); ok && rawID != "" {
		if idToken, verr := c.verifier.Verify(ctx, rawID); verr == nil {
			var claims OIDCClaims
			if cerr := idToken.Claims(&claims); cerr == nil {
				refreshed.IDToken = rawID
				refreshed.Claims = &claims
			}
		}
	}
	return refreshed, nil
}

// RevokeRefreshToken revokes a refresh token at the issuer's revocation
// endpoint (RFC 7009), authenticating as the client. Used best-effort at
// logout so the token family dies server-side instead of merely being
// forgotten locally.
func (c *OIDCClient) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	if err := c.ensureReady(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	form := url.Values{}
	form.Set("token", refreshToken)
	form.Set("token_type_hint", "refresh_token")
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.revocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("oidc: revoke refresh token: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("oidc: revoke refresh token: status %d", resp.StatusCode)
	}
	return nil
}

// EndSessionURL builds the RP-initiated logout URL on the issuer
// (OpenID Connect RP-Initiated Logout 1.0): id_token_hint identifies the IdP
// session to end, client_id + post_logout_redirect_uri bring the browser back
// to the configured post-logout landing.
func (c *OIDCClient) EndSessionURL(idTokenHint string) (string, error) {
	if err := c.ensureReady(context.Background()); err != nil {
		return "", err
	}
	params := url.Values{}
	if idTokenHint != "" {
		params.Set("id_token_hint", idTokenHint)
	}
	params.Set("client_id", c.cfg.ClientID)
	if c.cfg.PostLogoutRedirectURI != "" {
		params.Set("post_logout_redirect_uri", c.cfg.PostLogoutRedirectURI)
	}
	return c.endSessionEndpoint + "?" + params.Encode(), nil
}

// backchannelLogoutEvent is the member the `events` claim of a logout token
// must contain (OIDC Back-Channel Logout 1.0 §2.4).
const backchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// logoutTokenMaxAge bounds how old a logout token's iat may be. Tokens are
// minted per delivery attempt, so anything older is a replay.
const logoutTokenMaxAge = 2 * time.Minute

// LogoutClaims carries the session-targeting claims of a validated
// back-channel logout token. At least one of Subject/SessionID is non-empty.
type LogoutClaims struct {
	Subject   string
	SessionID string
}

// VerifyLogoutToken validates a back-channel logout token per OIDC
// Back-Channel Logout 1.0: signature + issuer + audience (= our client_id)
// via the issuer's JWKS, iat within logoutTokenMaxAge, the mandatory
// backchannel-logout `events` member, the mandatory absence of `nonce`, and
// the presence of at least one of sub/sid.
func (c *OIDCClient) VerifyLogoutToken(ctx context.Context, raw string) (LogoutClaims, error) {
	if err := c.ensureReady(ctx); err != nil {
		return LogoutClaims{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	tok, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		return LogoutClaims{}, fmt.Errorf("oidc: verify logout token: %w", err)
	}
	var claims struct {
		Sub    string                     `json:"sub"`
		Sid    string                     `json:"sid"`
		Iat    int64                      `json:"iat"`
		Nonce  *string                    `json:"nonce"`
		Events map[string]json.RawMessage `json:"events"`
	}
	if err := tok.Claims(&claims); err != nil {
		return LogoutClaims{}, fmt.Errorf("oidc: decode logout token claims: %w", err)
	}
	if claims.Nonce != nil {
		// The spec REQUIRES rejecting logout tokens with a nonce — it is what
		// distinguishes them from ID tokens, blocking cross-protocol replay.
		return LogoutClaims{}, errors.New("oidc: logout token must not contain nonce")
	}
	if _, ok := claims.Events[backchannelLogoutEvent]; !ok {
		return LogoutClaims{}, errors.New("oidc: logout token missing backchannel-logout event")
	}
	iat := time.Unix(claims.Iat, 0)
	now := time.Now()
	if claims.Iat == 0 || now.Sub(iat) > logoutTokenMaxAge || iat.Sub(now) > logoutTokenMaxAge {
		return LogoutClaims{}, errors.New("oidc: logout token iat outside acceptance window")
	}
	if claims.Sub == "" && claims.Sid == "" {
		return LogoutClaims{}, errors.New("oidc: logout token has neither sub nor sid")
	}
	return LogoutClaims{Subject: claims.Sub, SessionID: claims.Sid}, nil
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
	// Revocation + end-session endpoints are optional discovery fields; fall
	// back to iambarn's conventional paths when the document omits them.
	issuer := strings.TrimRight(c.cfg.Issuer, "/")
	var extra struct {
		RevocationEndpoint string `json:"revocation_endpoint"`
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	_ = prov.Claims(&extra)
	c.revocationEndpoint = extra.RevocationEndpoint
	if c.revocationEndpoint == "" {
		c.revocationEndpoint = issuer + "/oauth2/revoke"
	}
	c.endSessionEndpoint = extra.EndSessionEndpoint
	if c.endSessionEndpoint == "" {
		c.endSessionEndpoint = issuer + "/oauth2/end-session"
	}
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
		// offline_access asks iambarn for a refresh_token alongside the
		// short-lived (15m) access_token, so the session can be renewed
		// silently instead of forcing a full re-login every 15 minutes.
		// Requesting it here is required even though it's allowed on the
		// client record — iambarn only grants what's both allowed AND
		// explicitly requested.
		Scopes: []string{oidcv3.ScopeOpenID, "profile", "email", "offline_access"},
	}
	return nil
}

// OIDCClaims is the subset of ID-token / access-token claims this barn cares
// about.
type OIDCClaims struct {
	Subject           string   `json:"sub"`
	SessionID         string   `json:"sid"` // IdP session id; keys back-channel logout
	Email             string   `json:"email"`
	PreferredUsername string   `json:"preferred_username"`
	Name              string   `json:"name"`
	Picture           string   `json:"picture"`
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
