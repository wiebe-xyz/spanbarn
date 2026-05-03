package spanbarn

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPMiddleware(t *testing.T) {
	// Initialize a client that collects spans
	c := Init(Config{
		Endpoint:      "http://localhost:9999",
		APIKey:        "test-key",
		Service:       "test-svc",
		FlushInterval: 1 * time.Hour, // prevent auto-flush
	})
	defer Shutdown()

	handler := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify span context is in request context
		sc, ok := spanContextFromContext(r.Context())
		if !ok {
			t.Error("expected span context in request context")
		}
		if len(sc.TraceID) != 32 {
			t.Errorf("expected 32-char trace ID, got %d", len(sc.TraceID))
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Check that a span was enqueued
	time.Sleep(10 * time.Millisecond)
	if len(c.queue) != 1 {
		t.Fatalf("expected 1 span in queue, got %d", len(c.queue))
	}

	span := <-c.queue
	if span.Name != "GET /api/test" {
		t.Errorf("expected span name 'GET /api/test', got %q", span.Name)
	}
	if span.Kind != "server" {
		t.Errorf("expected kind 'server', got %q", span.Kind)
	}
	if span.Attributes["http.method"] != "GET" {
		t.Errorf("expected http.method=GET, got %v", span.Attributes["http.method"])
	}
	if span.Attributes["http.route"] != "/api/test" {
		t.Errorf("expected http.route=/api/test, got %v", span.Attributes["http.route"])
	}
	if span.Attributes["http.status_code"] != http.StatusOK {
		t.Errorf("expected http.status_code=200, got %v", span.Attributes["http.status_code"])
	}
}

func TestHTTPMiddlewareTraceparent(t *testing.T) {
	c := Init(Config{
		Endpoint:      "http://localhost:9999",
		APIKey:        "test-key",
		Service:       "test-svc",
		FlushInterval: 1 * time.Hour,
	})
	defer Shutdown()

	parentTraceID := "aaaa1111bbbb2222cccc3333dddd4444"
	parentSpanID := "1111222233334444"

	handler := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/data", nil)
	req.Header.Set("Traceparent", MakeTraceparent(parentTraceID, parentSpanID))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	time.Sleep(10 * time.Millisecond)

	span := <-c.queue
	if span.TraceID != parentTraceID {
		t.Errorf("expected trace ID %q, got %q", parentTraceID, span.TraceID)
	}
	if span.ParentSpanID != parentSpanID {
		t.Errorf("expected parent span ID %q, got %q", parentSpanID, span.ParentSpanID)
	}
}

func TestHTTPTransport(t *testing.T) {
	c := Init(Config{
		Endpoint:      "http://localhost:9999",
		APIKey:        "test-key",
		Service:       "test-svc",
		FlushInterval: 1 * time.Hour,
	})
	defer Shutdown()

	// Create a test server that checks for traceparent
	var gotTraceparent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("Traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := NewHTTPTransport(http.DefaultTransport)
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequest("GET", server.URL+"/external", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if gotTraceparent == "" {
		t.Error("expected traceparent header in outgoing request")
	}

	// Verify it's a valid traceparent
	traceID, spanID, ok := ParseTraceparent(gotTraceparent)
	if !ok {
		t.Fatalf("invalid traceparent: %q", gotTraceparent)
	}
	if len(traceID) != 32 || len(spanID) != 16 {
		t.Error("invalid traceparent IDs")
	}

	// Verify a span was enqueued
	time.Sleep(10 * time.Millisecond)
	if len(c.queue) != 1 {
		t.Fatalf("expected 1 span in queue, got %d", len(c.queue))
	}

	span := <-c.queue
	if span.Kind != "client" {
		t.Errorf("expected kind 'client', got %q", span.Kind)
	}
	if span.Attributes["http.method"] != "GET" {
		t.Errorf("expected http.method=GET, got %v", span.Attributes["http.method"])
	}
	if span.Attributes["http.status_code"] != http.StatusOK {
		t.Errorf("expected http.status_code=200, got %v", span.Attributes["http.status_code"])
	}
}
