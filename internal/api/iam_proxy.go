package api

import (
	"io"
	"net/http"
	"strings"
)

// handleIAMProxy forwards iambarn-profile widget API requests to IamBarn
// using the OIDC access token stored in the spanbarn_iam_token cookie.
// The widget's server-url points at /iam-proxy so all requests are
// same-origin to SpanBarn — no cross-origin cookie issues.
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

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "proxy error", "")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Authorization", "Bearer "+tokenCookie.Value)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "iambarn unreachable", "")
		return
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
