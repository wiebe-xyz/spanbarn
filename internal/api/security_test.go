package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	expectedHeaders := map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"X-XSS-Protection":       "0",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}

	for header, expected := range expectedHeaders {
		got := rr.Header().Get(header)
		if got != expected {
			t.Errorf("header %s: expected %q, got %q", header, expected, got)
		}
	}

	// HSTS should NOT be set for non-TLS.
	if rr.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS should not be set for non-TLS requests")
	}
}

func TestCSPHeader(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	expected := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'"
	got := rr.Header().Get("Content-Security-Policy")
	if got != expected {
		t.Errorf("CSP header: expected %q, got %q", expected, got)
	}
}

func TestHSTSOnTLS(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{} // Simulate TLS connection.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	expected := "max-age=31536000; includeSubDomains"
	got := rr.Header().Get("Strict-Transport-Security")
	if got != expected {
		t.Errorf("HSTS header: expected %q, got %q", expected, got)
	}
}

// TestHSTSOnForwardedProto covers the real deployment shape: TLS terminates at
// the proxy (r.TLS == nil) and the scheme arrives via X-Forwarded-Proto, which
// must still drive HSTS emission.
func TestHSTSOnForwardedProto(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("HSTS should be set when X-Forwarded-Proto is https")
	}
}

// TestIsSecureRequestTrustedProxies covers the SPANBARN_TRUSTED_PROXIES
// policy: X-Forwarded-Proto counts only from an allowlisted peer; everyone
// else gets the configured default.
func TestIsSecureRequestTrustedProxies(t *testing.T) {
	t.Cleanup(func() { SetSecurePolicy(nil, false) })

	newReq := func(remote, xfp string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xfp != "" {
			r.Header.Set("X-Forwarded-Proto", xfp)
		}
		return r
	}

	SetSecurePolicy([]string{"10.42.0.0/16", "192.168.1.9"}, true)

	cases := []struct {
		name   string
		remote string
		xfp    string
		want   bool
	}{
		{"trusted CIDR peer, https header", "10.42.3.7:41234", "https", true},
		{"trusted CIDR peer, http header", "10.42.3.7:41234", "http", false},
		{"trusted bare-IP peer, https header", "192.168.1.9:555", "https", true},
		{"untrusted peer spoofing http falls back to default", "203.0.113.5:99", "http", true},
		{"untrusted peer, no header, default", "203.0.113.5:99", "", true},
	}
	for _, tc := range cases {
		if got := isSecureRequest(newReq(tc.remote, tc.xfp)); got != tc.want {
			t.Errorf("%s: isSecureRequest = %v, want %v", tc.name, got, tc.want)
		}
	}

	// No allowlist + secure-by-default (non-dev): always secure, spoofed
	// "http" cannot strip the Secure flag.
	SetSecurePolicy(nil, true)
	if !isSecureRequest(newReq("203.0.113.5:99", "http")) {
		t.Error("secure-by-default must ignore a spoofed X-Forwarded-Proto: http")
	}

	// Legacy dev behaviour: no allowlist, not secure by default → header wins.
	SetSecurePolicy(nil, false)
	if !isSecureRequest(newReq("127.0.0.1:1", "https")) {
		t.Error("dev mode must honour X-Forwarded-Proto: https")
	}
	if isSecureRequest(newReq("127.0.0.1:1", "")) {
		t.Error("dev mode without header must be insecure")
	}
}
