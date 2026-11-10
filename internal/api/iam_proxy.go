package api

import (
	"bytes"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
)

// handleIAMProxy forwards iambarn-profile widget API requests to IamBarn
// using the OIDC access token stored on the caller's server-side session row
// (the route is wrapped in SessionMiddleware, so the row is on the context).
// The widget's server-url points at /iam-proxy so all requests are
// same-origin to SpanBarn — no cross-origin cookie issues.
//
// If IamBarn rejects the access token with 401, this invokes the shared
// session-refresh path once (singleflighted per session) and retries — the
// caller never sees the 401. Only a dead refresh token (invalid_grant, which
// also deletes the session row) surfaces as 401, which is what sends the SPA
// to the "sign in again" screen.
func (s *Server) handleIAMProxy(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeError(w, http.StatusNotFound, "oidc not configured", "")
		return
	}

	ws, ok := GetWebSession(r.Context())
	if !ok || ws.AuthMethod != "oidc" || ws.AccessToken == "" {
		writeError(w, http.StatusUnauthorized, "no iambarn token", "")
		return
	}

	targetURL, ok := s.iamProxyTarget(r)
	if !ok {
		writeError(w, http.StatusForbidden, "path not allowed", "")
		return
	}

	// Buffer the body so it can be replayed if a refresh+retry is needed —
	// the original r.Body can only be read once.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read request body", "")
		return
	}

	resp, err := s.proxyToIAM(r, targetURL, ws.AccessToken, body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "iambarn unreachable", "")
		return
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		// The stored access token is no longer good upstream (expired between
		// the middleware's check and this call, or centrally revoked). Run
		// the shared refresh path once and retry with the rotated token.
		refreshed, rerr := s.sessions.RefreshNow(r.Context(), sessionToken(r), ws.AccessToken)
		if rerr != nil || refreshed.AccessToken == "" {
			writeError(w, http.StatusUnauthorized, "iambarn session expired", "")
			return
		}
		resp, err = s.proxyToIAM(r, targetURL, refreshed.AccessToken, body)
		if err != nil {
			writeError(w, http.StatusBadGateway, "iambarn unreachable", "")
			return
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

// iamProxyTarget strips the /api/iam-proxy prefix and builds the URL on
// IamBarn. Only the endpoints the iambarn-profile widget calls, with the
// methods it uses, may be reached. This is a scoped proxy, not a relay for the
// user's bearer token to other IamBarn routes such as admin or organization
// switching.
func (s *Server) iamProxyTarget(r *http.Request) (string, bool) {
	p := strings.TrimPrefix(r.URL.Path, "/api/iam-proxy")
	if !iamProxyAllowed(r.Method, p) {
		return "", false
	}
	targetURL := strings.TrimRight(s.oidc.Config().Issuer, "/") + p
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}
	return targetURL, true
}

// iamProxyRoute is one widget endpoint. The pattern is matched against the
// whole decoded path; id segments are a single URL-safe token.
type iamProxyRoute struct {
	method  string
	pattern *regexp.Regexp
}

func iamRoute(method, pattern string) iamProxyRoute {
	return iamProxyRoute{method: method, pattern: regexp.MustCompile("^" + pattern + "$")}
}

// iamProxyRoutes is the endpoint list of the iambarn-profile widget
// (web/widget/src/api.ts in IamBarn). Add a route here when the widget gains
// one; nothing else under /api/ is forwarded.
var iamProxyRoutes = []iamProxyRoute{
	iamRoute(http.MethodGet, `/api/v1/csrf`),
	iamRoute(http.MethodGet, `/api/v1/me`),
	iamRoute(http.MethodPut, `/api/v1/me/profile`),
	iamRoute(http.MethodPost, `/api/v1/me/avatar`),
	iamRoute(http.MethodPost, `/api/v1/me/password`),
	iamRoute(http.MethodGet, `/api/v1/me/sessions`),
	iamRoute(http.MethodDelete, `/api/v1/me/sessions/[A-Za-z0-9_-]+`),
	iamRoute(http.MethodGet, `/api/v1/me/passkeys`),
	iamRoute(http.MethodDelete, `/api/v1/me/passkeys/[A-Za-z0-9_-]+`),
	iamRoute(http.MethodPost, `/api/v1/me/passkeys/register/begin`),
	iamRoute(http.MethodPost, `/api/v1/me/passkeys/register/finish`),
}

func iamProxyAllowed(method, p string) bool {
	// A path that changes under Clean has dot segments or doubled slashes.
	if path.Clean(p) != p {
		return false
	}
	for _, rt := range iamProxyRoutes {
		if rt.method == method && rt.pattern.MatchString(p) {
			return true
		}
	}
	return false
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
