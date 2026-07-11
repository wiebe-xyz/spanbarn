package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

// handleIAMProxy forwards iambarn-profile widget API requests to IamBarn
// using the OIDC access token stored in the spanbarn_iam_token cookie.
// The widget's server-url points at /iam-proxy so all requests are
// same-origin to SpanBarn — no cross-origin cookie issues.
//
// If IamBarn rejects the access token with 401, this transparently refreshes
// it via the spanbarn_iam_refresh cookie (when present) and retries once —
// the caller never sees the 401. Only a failed refresh (dead refresh token)
// surfaces as 401, which is what sends the SPA to the "sign in again" screen.
func (s *Server) handleIAMProxy(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeError(w, http.StatusNotFound, "oidc not configured", "")
		return
	}

	tokenCookie, err := r.Cookie("spanbarn_iam_token")
	if err != nil || tokenCookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "no iambarn token", "")
		return
	}

	// Strip the /api/iam-proxy prefix to get the path on IamBarn. Only the
	// widget's own API namespace may be reached — this is a scoped proxy for the
	// iambarn-profile widget, not an open relay for arbitrary IamBarn paths with
	// the user's bearer token.
	path := strings.TrimPrefix(r.URL.Path, "/api/iam-proxy")
	if !strings.HasPrefix(path, "/api/") {
		writeError(w, http.StatusForbidden, "path not allowed", "")
		return
	}
	issuer := strings.TrimRight(s.oidc.Config().Issuer, "/")
	targetURL := issuer + path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Buffer the body so it can be replayed if a refresh+retry is needed —
	// the original r.Body can only be read once.
	var body []byte
	if r.Body != nil {
		body, err = io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "read request body", "")
			return
		}
	}

	resp, err := s.proxyToIAM(r, targetURL, tokenCookie.Value, body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "iambarn unreachable", "")
		return
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if refreshed, ok := s.tryRefreshIAMToken(w, r); ok {
			resp, err = s.proxyToIAM(r, targetURL, refreshed, body)
			if err != nil {
				writeError(w, http.StatusBadGateway, "iambarn unreachable", "")
				return
			}
		}
	}
	defer resp.Body.Close()

	// Copy only a safe allowlist of response headers. Notably never forward
	// Set-Cookie (which would let IamBarn set cookies on the SpanBarn origin) or
	// hop-by-hop headers.
	for _, h := range proxyResponseHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// proxyToIAM issues one proxied request to IamBarn with the given bearer
// token, replaying body on each call (retries reuse the same buffered bytes).
func (s *Server) proxyToIAM(r *http.Request, targetURL, bearer string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	return http.DefaultClient.Do(req)
}

// tryRefreshIAMToken attempts a silent token refresh using the
// spanbarn_iam_refresh cookie. On success it rewrites both iambarn cookies
// with the rotated pair and returns the new access token. On a definitive
// invalid_grant failure (dead refresh token) it clears both cookies so the
// next request cleanly surfaces the "sign in again" screen instead of
// retrying a token that's gone. On a transient error (e.g. network timeout)
// it leaves the stored refresh token untouched — retrying an ambiguous
// refresh call with the same token risks a replay, so callers get one 401
// today and try again on the next request instead.
func (s *Server) tryRefreshIAMToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	refreshCookie, err := r.Cookie("spanbarn_iam_refresh")
	if err != nil || refreshCookie.Value == "" {
		return "", false
	}

	// singleflight collapses concurrent requests that hit 401 with the same
	// refresh_token into one token-endpoint call — see Server.iamRefresh.
	v, err, _ := s.iamRefresh.Do(refreshCookie.Value, func() (any, error) {
		return s.oidc.Refresh(r.Context(), refreshCookie.Value)
	})
	if err != nil {
		if errors.Is(err, auth.ErrRefreshInvalid) {
			s.logger.Info("oidc: refresh token invalid, clearing iambarn session")
			clearIAMTokenCookies(w, isSecureRequest(r))
		} else {
			s.logger.Warn("oidc: refresh failed", "error", err)
		}
		return "", false
	}

	refreshed := v.(auth.RefreshedTokens)
	setIAMTokenCookies(w, refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt, isSecureRequest(r))
	return refreshed.AccessToken, true
}

// proxyResponseHeaders is the set of upstream response headers the IAM proxy
// echoes back to the browser. Everything else (Set-Cookie, hop-by-hop headers,
// server fingerprints) is dropped.
var proxyResponseHeaders = []string{
	"Content-Type",
	"Content-Length",
	"Cache-Control",
	"ETag",
	"Last-Modified",
	"Vary",
}
