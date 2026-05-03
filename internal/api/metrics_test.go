package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsEndpoint(t *testing.T) {
	m := NewMetrics()
	handler := m.Handler("") // No token required.

	// Increment a counter so there's something to expose.
	m.SpansIngested.Add(42)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "spans_ingested_total") {
		t.Error("expected spans_ingested_total in response")
	}
	if !strings.Contains(body, "42") {
		t.Error("expected counter value 42 in response")
	}
}

func TestMetricsEndpointWithToken(t *testing.T) {
	m := NewMetrics()
	token := "secret-metrics-token"
	handler := m.Handler(token)

	// Request without token => 401.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rr.Code)
	}

	// Request with wrong token => 401.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", rr.Code)
	}

	// Request with correct token => 200.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct token, got %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "spans_ingested_total") {
		t.Error("expected spans_ingested_total in response")
	}
}

func TestMetricsMiddleware(t *testing.T) {
	m := NewMetrics()
	mw := MetricsMiddleware(m)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Verify the metrics handler reports the request.
	metricsHandler := m.Handler("")
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRR := httptest.NewRecorder()
	metricsHandler.ServeHTTP(metricsRR, metricsReq)

	body := metricsRR.Body.String()
	if !strings.Contains(body, "http_requests_total") {
		t.Error("expected http_requests_total in metrics")
	}
	if !strings.Contains(body, "http_request_duration_seconds") {
		t.Error("expected http_request_duration_seconds in metrics")
	}
}
