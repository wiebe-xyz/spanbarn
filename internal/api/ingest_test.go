package api_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

const testAPIKey = "test-key-123"

func setupTestServer(t *testing.T) (*httptest.Server, *ingest.Queue) {
	t.Helper()
	q := ingest.NewQueue(1024)
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })

	h := ingest.NewHandler(q, sp, 0, slog.Default())
	h.Start(t.Context())
	t.Cleanup(func() { h.Stop() })

	srv := api.NewServer(api.ServerConfig{
		APIKey:       testAPIKey,
		MaxBodyBytes: 4 << 20,
		Version:      "test",
	}, h, slog.Default())

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, q
}

func validSpanJSON() string {
	batch := api.SpanBatch{
		Spans: []api.SpanInput{
			{
				TraceID:   "abc123",
				SpanID:    "span1",
				Name:      "GET /users",
				Service:   "api-gateway",
				StartTime: 1700000000000,
				Duration:  5000,
			},
		},
	}
	data, _ := json.Marshal(batch)
	return string(data)
}

func TestIngestValidBatch(t *testing.T) {
	ts, _ := setupTestServer(t)

	batch := api.SpanBatch{
		Spans: []api.SpanInput{
			{
				TraceID:   "abc123",
				SpanID:    "span1",
				Name:      "GET /users",
				Service:   "api-gateway",
				StartTime: 1700000000000,
				Duration:  5000,
			},
			{
				TraceID:   "abc123",
				SpanID:    "span2",
				Name:      "SELECT users",
				Service:   "user-service",
				StartTime: 1700000000001,
				Duration:  3000,
			},
		},
	}
	body, _ := json.Marshal(batch)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	var result api.IngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Accepted != 2 {
		t.Fatalf("expected accepted=2, got %d", result.Accepted)
	}
}

func TestIngestMissingFields(t *testing.T) {
	ts, _ := setupTestServer(t)

	batch := api.SpanBatch{
		Spans: []api.SpanInput{
			{
				TraceID: "abc123",
				// Missing spanId, name, service, startTime, duration
			},
		},
	}
	body, _ := json.Marshal(batch)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	var result api.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Error != "validation failed" {
		t.Fatalf("expected 'validation failed', got %q", result.Error)
	}
	if !strings.Contains(result.Details, "spanId") {
		t.Fatalf("expected details to mention spanId, got %q", result.Details)
	}
}

func TestIngestEmptyBatch(t *testing.T) {
	ts, _ := setupTestServer(t)

	body := []byte(`{"spans":[]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	var result api.IngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Accepted != 0 {
		t.Fatalf("expected accepted=0, got %d", result.Accepted)
	}
}

func TestIngestInvalidJSON(t *testing.T) {
	ts, _ := setupTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", strings.NewReader("{garbage"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestIngestBodyTooLarge(t *testing.T) {
	// Set up a server with a very small body limit.
	q := ingest.NewQueue(1024)
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })

	h := ingest.NewHandler(q, sp, 0, slog.Default())
	h.Start(t.Context())
	t.Cleanup(func() { h.Stop() })

	srv := api.NewServer(api.ServerConfig{
		APIKey:       testAPIKey,
		MaxBodyBytes: 64, // Very small limit
		Version:      "test",
	}, h, slog.Default())

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Send a body larger than 64 bytes.
	largeBody := strings.Repeat("x", 200)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", strings.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Accept either 400 (our handler catches it) or 413 (MaxBytesReader).
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 400 or 413, got %d", resp.StatusCode)
	}
}

func TestIngestNoAPIKey(t *testing.T) {
	ts, _ := setupTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", strings.NewReader(validSpanJSON()))
	req.Header.Set("Content-Type", "application/json")
	// No API key header

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestIngestWrongAPIKey(t *testing.T) {
	ts, _ := setupTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", strings.NewReader(validSpanJSON()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", "wrong-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestIngestResourceExtraction(t *testing.T) {
	// Use a handler without starting the flush loop so the queue is not drained.
	q := ingest.NewQueue(1024)
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })

	// Do NOT call h.Start() — we want records to stay in the queue.
	h := ingest.NewHandler(q, sp, 0, slog.Default())

	srv := api.NewServer(api.ServerConfig{
		APIKey:       testAPIKey,
		MaxBodyBytes: 4 << 20,
		Version:      "test",
	}, h, slog.Default())

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	attrs := map[string]any{"http.route": "/api/users/{id}"}
	attrsJSON, _ := json.Marshal(attrs)

	batch := api.SpanBatch{
		Spans: []api.SpanInput{
			{
				TraceID:    "abc123",
				SpanID:     "span1",
				Name:       "HTTP GET",
				Service:    "api-gateway",
				StartTime:  1700000000000,
				Duration:   5000,
				Attributes: attrsJSON,
				// No Resource field — should be extracted from http.route.
			},
		},
	}
	body, _ := json.Marshal(batch)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	// Drain the queue to check the resource was extracted.
	records := q.Drain()
	if len(records) != 1 {
		t.Fatalf("expected 1 record in queue, got %d", len(records))
	}
	if records[0].Resource != "/api/users/{id}" {
		t.Fatalf("expected resource '/api/users/{id}', got %q", records[0].Resource)
	}
}
