package api

import (
	"net/http"
	"regexp"
	"strings"
)

// defaultPublicURL is the canonical dashboard host, used when SPANBARN_PUBLIC_URL
// is unset.
const defaultPublicURL = "https://spanbarn.wiebe.xyz"

// brandedIngestHost matches a product-branded SpanBarn alias: "sb." followed by
// a registrable domain (sb.acme.nl, sb.eu.acme.com). The production
// IngressRoute sends ingest and setup for every host of this shape to SpanBarn,
// so a host that matches here is one a client can actually reach.
var brandedIngestHost = regexp.MustCompile(`^sb\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// canonicalURL is the dashboard base URL without a trailing slash.
func (s *Server) canonicalURL() string {
	if s.publicURL == "" {
		return defaultPublicURL
	}
	return strings.TrimSuffix(s.publicURL, "/")
}

// ingestBaseURL is the base URL that onboarding output points ingest at. A
// request that arrived on a branded "sb." alias gets that alias back, so the
// setup guide for sb.acme.nl tells the client to send telemetry to sb.acme.nl.
// Every other host gets the canonical URL. Branded hosts are production-only
// and always sit behind TLS, hence the fixed https scheme.
//
// The host is only echoed when it matches brandedIngestHost, so a forged Host
// header cannot inject arbitrary text or a non-sb. host into the page.
func (s *Server) ingestBaseURL(r *http.Request) string {
	if host := requestHost(r); brandedIngestHost.MatchString(host) {
		return "https://" + host
	}
	return s.canonicalURL()
}

// requestHost is the host the client addressed, lowercased and without a port.
// X-Forwarded-Host is honoured only when proxy headers are trusted
// (SPANBARN_TRUST_PROXY); otherwise the request's own Host is used.
func requestHost(r *http.Request) string {
	host := r.Host
	if trustProxyHeaders.Load() {
		if fwd := strings.TrimSpace(strings.SplitN(r.Header.Get("X-Forwarded-Host"), ",", 2)[0]); fwd != "" {
			host = fwd
		}
	}
	return strings.ToLower(hostWithoutPort(host))
}

func hostWithoutPort(h string) string {
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		return h[:i]
	}
	return h
}
