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
		"X-XSS-Protection":      "0",
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
