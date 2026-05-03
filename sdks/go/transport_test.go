package spanbarn

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTransportSend(t *testing.T) {
	var receivedBody sendPayload
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tr := newTransport(server.URL, "test-api-key")
	spans := []*SpanData{
		{
			TraceID:   "aaaa1111bbbb2222cccc3333dddd4444",
			SpanID:    "1111222233334444",
			Name:      "test-span",
			Service:   "test-svc",
			Kind:      "internal",
			Status:    "ok",
			StartTime: time.Now().UnixMicro(),
			Duration:  1000,
		},
	}

	err := tr.Send(spans)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify headers
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", receivedHeaders.Get("Content-Type"))
	}
	if receivedHeaders.Get("X-SpanBarn-Api-Key") != "test-api-key" {
		t.Errorf("expected X-SpanBarn-Api-Key header, got %q", receivedHeaders.Get("X-SpanBarn-Api-Key"))
	}

	// Verify body
	if len(receivedBody.Spans) != 1 {
		t.Fatalf("expected 1 span in body, got %d", len(receivedBody.Spans))
	}
	if receivedBody.Spans[0].Name != "test-span" {
		t.Errorf("expected span name 'test-span', got %q", receivedBody.Spans[0].Name)
	}
}

func TestTransportSendEmpty(t *testing.T) {
	tr := newTransport("http://localhost:9999", "key")
	err := tr.Send(nil)
	if err != nil {
		t.Errorf("expected no error for empty batch, got %v", err)
	}
}

func TestTransportSendFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tr := newTransport(server.URL, "test-api-key")
	spans := []*SpanData{
		{Name: "fail-span", StartTime: time.Now().UnixMicro()},
	}

	err := tr.Send(spans)
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestTransportTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tr := newTransport(server.URL, "test-api-key")
	// Override with a short timeout for testing
	tr.client = &http.Client{Timeout: 50 * time.Millisecond}

	spans := []*SpanData{
		{Name: "timeout-span", StartTime: time.Now().UnixMicro()},
	}

	err := tr.Send(spans)
	if err == nil {
		t.Error("expected timeout error")
	}
}
