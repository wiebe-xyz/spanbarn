package api

import (
	"net"
	"net/http"
	"strings"
)

// securePolicy controls how isSecureRequest interprets X-Forwarded-Proto.
// Configured once at startup via SetSecurePolicy; the zero value (no trusted
// proxies, secureByDefault=false) preserves the permissive legacy behaviour
// for tests and bare dev runs: trust the header from anyone.
var securePolicy struct {
	trustedProxies  []*net.IPNet
	secureByDefault bool
}

// SetSecurePolicy configures the trusted-proxy allowlist for the
// X-Forwarded-Proto header (SPANBARN_TRUSTED_PROXIES) and the fallback when
// the header cannot be trusted. cidrs entries may be CIDRs ("10.42.0.0/16")
// or bare IPs ("10.42.0.7"); invalid entries are ignored. secureByDefault
// should be true outside dev — every named deployment terminates TLS at the
// proxy, so cookies must default to Secure even when the proxy's identity is
// not pinned. Call before serving; not synchronized.
func SetSecurePolicy(cidrs []string, secureByDefault bool) {
	securePolicy.trustedProxies = nil
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		if _, ipnet, err := net.ParseCIDR(c); err == nil {
			securePolicy.trustedProxies = append(securePolicy.trustedProxies, ipnet)
		}
	}
	securePolicy.secureByDefault = secureByDefault
}

// isSecureRequest reports whether the original client connection used HTTPS.
// TLS terminates at the reverse proxy (Caddy/Nginx/Traefik) in every real
// deployment, so r.TLS is nil for the request the app sees; the proxy conveys
// the real scheme via X-Forwarded-Proto. That header is only honoured when it
// arrives from a trusted peer:
//
//   - trusted-proxy CIDRs configured: the header counts only if the direct
//     peer (RemoteAddr) is inside the allowlist; otherwise the configured
//     default applies.
//   - no CIDRs configured, secure-by-default (any non-dev environment):
//     always true — deployments sit behind TLS, and a client-spoofed
//     "X-Forwarded-Proto: http" must not strip the Secure flag off cookies.
//   - no CIDRs, dev: legacy behaviour, trust the header from anyone (it only
//     affects the spoofer's own cookies on a plaintext dev server).
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	forwardedHTTPS := strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	if len(securePolicy.trustedProxies) > 0 {
		if peerIsTrustedProxy(r.RemoteAddr) {
			return forwardedHTTPS
		}
		return securePolicy.secureByDefault
	}
	if securePolicy.secureByDefault {
		return true
	}
	return forwardedHTTPS
}

// peerIsTrustedProxy reports whether the direct TCP peer is inside the
// configured trusted-proxy allowlist.
func peerIsTrustedProxy(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, ipnet := range securePolicy.trustedProxies {
		if ipnet.Contains(ip) {
			return true
		}
	}
	return false
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
