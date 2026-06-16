package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const oidcScope = "openid profile email offline_access"

// oidcDiscovery is the subset of the issuer's discovery document the CLI needs.
type oidcDiscovery struct {
	TokenEndpoint       string `json:"token_endpoint"`
	DeviceAuthEndpoint  string `json:"device_authorization_endpoint"`
	AuthorizationEndpnt string `json:"authorization_endpoint"`
}

// spanbarnOIDCConfig is the OIDC block from SpanBarn's /api/v1/client-config.
type spanbarnOIDCConfig struct {
	Issuer      string
	CLIClientID string
}

// tokenResponse is the OAuth2 token endpoint response (success or error).
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func httpClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }

// fetchSpanbarnOIDC reads issuer + cli_client_id from SpanBarn's public
// client-config so `sb login --oidc` only needs --url.
func fetchSpanbarnOIDC(base string) (spanbarnOIDCConfig, error) {
	resp, err := httpClient().Get(strings.TrimRight(base, "/") + "/api/v1/client-config")
	if err != nil {
		return spanbarnOIDCConfig{}, fmt.Errorf("fetch client-config: %w", err)
	}
	defer resp.Body.Close()
	var cc struct {
		OIDC struct {
			Enabled     bool   `json:"enabled"`
			Issuer      string `json:"issuer"`
			CLIClientID string `json:"cli_client_id"`
		} `json:"oidc"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cc); err != nil {
		return spanbarnOIDCConfig{}, fmt.Errorf("decode client-config: %w", err)
	}
	if !cc.OIDC.Enabled || cc.OIDC.Issuer == "" {
		return spanbarnOIDCConfig{}, fmt.Errorf("this SpanBarn instance does not have OIDC enabled")
	}
	return spanbarnOIDCConfig{Issuer: cc.OIDC.Issuer, CLIClientID: cc.OIDC.CLIClientID}, nil
}

func discoverOIDC(issuer string) (oidcDiscovery, error) {
	resp, err := httpClient().Get(strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration")
	if err != nil {
		return oidcDiscovery{}, fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	var d oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return oidcDiscovery{}, fmt.Errorf("decode discovery: %w", err)
	}
	if d.TokenEndpoint == "" {
		return oidcDiscovery{}, fmt.Errorf("issuer %q has no token_endpoint", issuer)
	}
	return d, nil
}

// deviceLogin runs the RFC 8628 device authorization grant against the SpanBarn
// instance's IamBarn issuer and fills the OIDC fields of cfg on success.
func deviceLogin(cfg *Config) error {
	sc, err := fetchSpanbarnOIDC(cfg.URL)
	if err != nil {
		return err
	}
	if sc.CLIClientID == "" {
		return fmt.Errorf("this SpanBarn instance has no oidc.cli_client_id configured; ask the operator to set SPANBARN_OIDC_CLI_CLIENT_ID")
	}
	disco, err := discoverOIDC(sc.Issuer)
	if err != nil {
		return err
	}
	if disco.DeviceAuthEndpoint == "" {
		return fmt.Errorf("issuer %q does not support the device authorization grant", sc.Issuer)
	}

	// 1. Request a device + user code.
	form := url.Values{"client_id": {sc.CLIClientID}, "scope": {oidcScope}}
	resp, err := httpClient().PostForm(disco.DeviceAuthEndpoint, form)
	if err != nil {
		return fmt.Errorf("device authorization: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("device authorization failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var da struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int64  `json:"expires_in"`
		Interval                int64  `json:"interval"`
	}
	if err := json.Unmarshal(body, &da); err != nil {
		return fmt.Errorf("decode device authorization: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\nTo sign in, open:\n  %s\n", da.VerificationURI)
	fmt.Fprintf(os.Stderr, "and enter code:  %s\n", da.UserCode)
	if da.VerificationURIComplete != "" {
		fmt.Fprintf(os.Stderr, "(or open directly: %s)\n", da.VerificationURIComplete)
	}
	fmt.Fprintln(os.Stderr, "\nWaiting for approval…")

	// 2. Poll the token endpoint.
	interval := da.Interval
	if interval <= 0 {
		interval = 5
	}
	deadline := time.Now().Add(time.Duration(da.ExpiresIn) * time.Second)
	if da.ExpiresIn == 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(interval) * time.Second)
		tok, err := postToken(disco.TokenEndpoint, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {da.DeviceCode},
			"client_id":   {sc.CLIClientID},
		}, "", "")
		if err != nil {
			return err
		}
		switch tok.Error {
		case "":
			cfg.AuthType = "oidc-device"
			cfg.OIDCIssuer = sc.Issuer
			cfg.OIDCClientID = sc.CLIClientID
			applyToken(cfg, tok)
			return nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5
		default:
			return fmt.Errorf("device login failed: %s (%s)", tok.Error, tok.ErrorDescription)
		}
	}
	return fmt.Errorf("device login timed out before approval")
}

// clientCredentialsLogin performs the M2M client_credentials grant.
func clientCredentialsLogin(cfg *Config, issuer, clientID, clientSecret, scope string) error {
	if issuer == "" {
		sc, err := fetchSpanbarnOIDC(cfg.URL)
		if err != nil {
			return err
		}
		issuer = sc.Issuer
	}
	disco, err := discoverOIDC(issuer)
	if err != nil {
		return err
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	if scope != "" {
		form.Set("scope", scope)
	}
	tok, err := postToken(disco.TokenEndpoint, form, clientID, clientSecret)
	if err != nil {
		return err
	}
	if tok.Error != "" {
		return fmt.Errorf("client_credentials failed: %s (%s)", tok.Error, tok.ErrorDescription)
	}
	cfg.AuthType = "oidc-m2m"
	cfg.OIDCIssuer = issuer
	cfg.OIDCClientID = clientID
	cfg.OIDCClientSecret = clientSecret
	applyToken(cfg, tok)
	return nil
}

// refreshOIDCToken obtains a fresh access token for an expired session, using
// the refresh token (device) or re-running client_credentials (m2m).
func refreshOIDCToken(cfg *Config) error {
	disco, err := discoverOIDC(cfg.OIDCIssuer)
	if err != nil {
		return err
	}
	switch cfg.AuthType {
	case "oidc-m2m":
		return clientCredentialsLogin(cfg, cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCClientSecret, "")
	case "oidc-device":
		if cfg.RefreshToken == "" {
			return fmt.Errorf("session expired and no refresh token; run: sb login --oidc")
		}
		tok, err := postToken(disco.TokenEndpoint, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {cfg.RefreshToken},
			"client_id":     {cfg.OIDCClientID},
		}, "", "")
		if err != nil {
			return err
		}
		if tok.Error != "" {
			return fmt.Errorf("token refresh failed: %s; run: sb login --oidc", tok.Error)
		}
		applyToken(cfg, tok)
		return nil
	default:
		return fmt.Errorf("not an OIDC session")
	}
}

// postToken posts a form to the token endpoint, optionally with HTTP Basic
// client authentication (client_secret_basic).
func postToken(endpoint string, form url.Values, clientID, clientSecret string) (tokenResponse, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if clientID != "" {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token response (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return tr, nil
}

// applyToken stores a token response into cfg.
func applyToken(cfg *Config, tok tokenResponse) {
	cfg.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		cfg.RefreshToken = tok.RefreshToken
	}
	if tok.ExpiresIn > 0 {
		cfg.TokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix()
	} else {
		cfg.TokenExpiry = 0
	}
}
