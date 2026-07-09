package api

import (
	"net/http"
	"strings"
)

// isSecureRequest reports whether the original client connection used HTTPS.
// TLS terminates at the reverse proxy (Caddy/Nginx) in every real deployment, so
// r.TLS is nil for the request the app sees; the proxy conveys the real scheme
// via X-Forwarded-Proto. Honouring that header only decides whether the Secure
// cookie flag and HSTS are emitted, so a spoofed value can at most affect the
// spoofer's own session — and browsers ignore an HSTS header received over
// plaintext — which makes it safe to trust here without a proxy allowlist.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// SecurityHeaders adds standard security headers to all responses.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-XSS-Protection", "0")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'")
		if isSecureRequest(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}
