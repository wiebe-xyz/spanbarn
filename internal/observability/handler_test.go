package observability

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBugBarnHandler_ForwardsWarnAndError(t *testing.T) {
	var mu sync.Mutex
	var received []bugbarnEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev bugbarnEvent
		if err := json.Unmarshal(body, &ev); err == nil {
			mu.Lock()
			received = append(received, ev)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	client := NewBugBarnClient(BugBarnConfig{
		Endpoint:    srv.URL,
		APIKey:      "test-key",
		Project:     "spanbarn",
		Environment: "test",
		Version:     "dev",
	})

	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewBugBarnHandler(jsonHandler, client)
	logger := slog.New(handler)

	logger.Info("this should not forward")
	logger.Warn("warning message", "key", "val")
	logger.Error("error message", "code", 500)

	client.Shutdown()

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 2 {
		t.Fatalf("expected 2 events forwarded, got %d", len(received))
	}
	if received[0].Level != "WARN" {
		t.Errorf("expected WARN level, got %s", received[0].Level)
	}
	if received[0].Message != "warning message" {
		t.Errorf("expected 'warning message', got %s", received[0].Message)
	}
	if received[1].Level != "ERROR" {
		t.Errorf("expected ERROR level, got %s", received[1].Level)
	}
}

func TestBugBarnHandler_ErrorAttrSerializedAsString(t *testing.T) {
	var mu sync.Mutex
	var received []bugbarnEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev bugbarnEvent
		if err := json.Unmarshal(body, &ev); err == nil {
			mu.Lock()
			received = append(received, ev)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	client := NewBugBarnClient(BugBarnConfig{
		Endpoint: srv.URL,
		APIKey:   "test-key",
		Project:  "spanbarn",
	})

	var buf bytes.Buffer
	handler := NewBugBarnHandler(slog.NewJSONHandler(&buf, nil), client)
	logger := slog.New(handler)

	logger.Error("insert failed", "error", io.ErrUnexpectedEOF)
	client.Shutdown()

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	errAttr, ok := received[0].Attributes["error"]
	if !ok {
		t.Fatal("expected 'error' attribute")
	}
	errStr, ok := errAttr.(string)
	if !ok {
		t.Fatalf("expected error attribute to be string, got %T", errAttr)
	}
	if errStr != "unexpected EOF" {
		t.Errorf("expected 'unexpected EOF', got %q", errStr)
	}
}

func TestBugBarnClient_CaptureError(t *testing.T) {
	var mu sync.Mutex
	var received []bugbarnEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-BugBarn-Api-Key") != "test-key" {
			t.Error("missing or wrong API key header")
		}
		if r.Header.Get("X-BugBarn-Project") != "spanbarn" {
			t.Error("missing or wrong project header")
		}
		body, _ := io.ReadAll(r.Body)
		var ev bugbarnEvent
		if err := json.Unmarshal(body, &ev); err == nil {
			mu.Lock()
			received = append(received, ev)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	client := NewBugBarnClient(BugBarnConfig{
		Endpoint:    srv.URL,
		APIKey:      "test-key",
		Project:     "spanbarn",
		Environment: "production",
		Version:     "1.0.0",
	})

	client.CaptureError(io.ErrUnexpectedEOF, map[string]any{"context": "reading body"})
	client.Shutdown()

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	ev := received[0]
	if ev.Type != "exception" {
		t.Errorf("expected type 'exception', got %s", ev.Type)
	}
	if ev.Exception == nil {
		t.Fatal("expected exception data")
	}
	if ev.Exception.Value != "unexpected EOF" {
		t.Errorf("expected 'unexpected EOF', got %s", ev.Exception.Value)
	}
	if ev.Tags["environment"] != "production" {
		t.Errorf("expected environment tag 'production', got %s", ev.Tags["environment"])
	}
}

func TestTracingMiddleware_SkipsHealthAndMetrics(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := TracingMiddleware(inner)

	for _, path := range []string{"/api/v1/health", "/metrics"} {
		called = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if !called {
			t.Errorf("expected inner handler to be called for %s", path)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 for %s, got %d", path, rec.Code)
		}
	}
}

func TestSetup_ReturnsLoggerWithoutConfig(t *testing.T) {
	// With no env vars set, Setup should still return a functional logger
	logger, shutdown := Setup("test")
	defer shutdown()

	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
	logger.Info("test message")
}

// Ensure the flush ticker doesn't block shutdown.
func TestBugBarnClient_ShutdownWithoutEvents(t *testing.T) {
	client := NewBugBarnClient(BugBarnConfig{
		Endpoint: "http://localhost:0",
		APIKey:   "key",
		Project:  "test",
	})

	done := make(chan struct{})
	go func() {
		client.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timed out")
	}
}
