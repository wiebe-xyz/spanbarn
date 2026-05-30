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

	// Strip the /api/iam-proxy prefix to get the path on IamBarn.
	path := strings.TrimPrefix(r.URL.Path, "/api/iam-proxy")
	if path == "" {
		path = "/"
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

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
