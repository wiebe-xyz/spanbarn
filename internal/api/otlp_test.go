package api_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"log/slog"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/spool"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// setupOTLPTestServer creates a test server and returns it along with the queue
// for inspecting enqueued records. The ingest handler flush loop is NOT started
// so records remain in the queue for inspection.
func setupOTLPTestServer(t *testing.T) (*httptest.Server, *ingest.Queue) {
	t.Helper()
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
	return ts, q
}

// buildOTLPRequest constructs a minimal ExportTraceServiceRequest with one span.
func buildOTLPRequest(serviceName string, spanName string, kind tracepb.Span_SpanKind, status *tracepb.Status, startNano, endNano uint64) *collectorpb.ExportTraceServiceRequest {
	traceID := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	spanID := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11}
	parentSpanID := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}

	return &collectorpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{
							Key:   "service.name",
							Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: serviceName}},
						},
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Spans: []*tracepb.Span{
							{
								TraceId:           traceID,
								SpanId:            spanID,
								ParentSpanId:      parentSpanID,
								Name:              spanName,
								Kind:              kind,
								Status:            status,
								StartTimeUnixNano: startNano,
								EndTimeUnixNano:   endNano,
							},
						},
					},
				},
			},
		},
	}
}

func TestOTLPIngestProtobuf(t *testing.T) {
	ts, q := setupOTLPTestServer(t)

	otlpReq := buildOTLPRequest("my-service", "GET /users", tracepb.Span_SPAN_KIND_SERVER, nil, 1700000000000000, 1700000005000000)
	body, err := proto.Marshal(otlpReq)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	records := q.Drain()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Name != "GET /users" {
		t.Fatalf("expected name 'GET /users', got %q", records[0].Name)
	}
	if records[0].TraceID != "0102030405060708090a0b0c0d0e0f10" {
		t.Fatalf("unexpected traceId: %s", records[0].TraceID)
	}
	if records[0].SpanID != "aabbccddeeff0011" {
		t.Fatalf("unexpected spanId: %s", records[0].SpanID)
	}
	if records[0].ParentSpanID != "1122334455667788" {
		t.Fatalf("unexpected parentSpanId: %s", records[0].ParentSpanID)
	}
}

func TestOTLPIngestJSON(t *testing.T) {
	ts, q := setupOTLPTestServer(t)

	otlpReq := buildOTLPRequest("json-service", "POST /api", tracepb.Span_SPAN_KIND_CLIENT, nil, 1000000000, 2000000000)
	body, err := protojson.Marshal(otlpReq)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	records := q.Drain()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Name != "POST /api" {
		t.Fatalf("expected name 'POST /api', got %q", records[0].Name)
	}
	if records[0].Kind != "client" {
		t.Fatalf("expected kind 'client', got %q", records[0].Kind)
	}
}

func TestOTLPServiceNameExtraction(t *testing.T) {
	ts, q := setupOTLPTestServer(t)

	otlpReq := buildOTLPRequest("payment-service", "process-payment", tracepb.Span_SPAN_KIND_INTERNAL, nil, 1000000000, 2000000000)
	body, _ := proto.Marshal(otlpReq)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	records := q.Drain()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Service != "payment-service" {
		t.Fatalf("expected service 'payment-service', got %q", records[0].Service)
	}
}

func TestOTLPSpanKindMapping(t *testing.T) {
	tests := []struct {
		kind     tracepb.Span_SpanKind
		expected string
	}{
		{tracepb.Span_SPAN_KIND_SERVER, "server"},
		{tracepb.Span_SPAN_KIND_CLIENT, "client"},
		{tracepb.Span_SPAN_KIND_INTERNAL, "internal"},
		{tracepb.Span_SPAN_KIND_PRODUCER, "producer"},
		{tracepb.Span_SPAN_KIND_CONSUMER, "consumer"},
		{tracepb.Span_SPAN_KIND_UNSPECIFIED, "internal"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			ts, q := setupOTLPTestServer(t)

			otlpReq := buildOTLPRequest("svc", "op", tt.kind, nil, 1000000000, 2000000000)
			body, _ := proto.Marshal(otlpReq)

			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/traces", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/x-protobuf")
			req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d", resp.StatusCode)
			}

			records := q.Drain()
			if len(records) != 1 {
				t.Fatalf("expected 1 record, got %d", len(records))
			}
			if records[0].Kind != tt.expected {
				t.Fatalf("expected kind %q, got %q", tt.expected, records[0].Kind)
			}
		})
	}
}

func TestOTLPStatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		status   *tracepb.Status
		expected string
	}{
		{"ok", &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK}, "ok"},
		{"error", &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}, "error"},
		{"unset", &tracepb.Status{Code: tracepb.Status_STATUS_CODE_UNSET}, "unset"},
		{"nil", nil, "unset"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, q := setupOTLPTestServer(t)

			otlpReq := buildOTLPRequest("svc", "op", tracepb.Span_SPAN_KIND_SERVER, tt.status, 1000000000, 2000000000)
			body, _ := proto.Marshal(otlpReq)

			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/traces", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/x-protobuf")
			req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d", resp.StatusCode)
			}

			records := q.Drain()
			if len(records) != 1 {
				t.Fatalf("expected 1 record, got %d", len(records))
			}
			if records[0].Status != tt.expected {
				t.Fatalf("expected status %q, got %q", tt.expected, records[0].Status)
			}
		})
	}
}

func TestOTLPTimestampConversion(t *testing.T) {
	ts, q := setupOTLPTestServer(t)

	// 1_700_000_000_000_000 ns = 1_700_000_000_000 us (start)
	// 1_700_000_005_000_000 ns = 1_700_000_005_000 us (end)
	// duration = 5_000 us
	startNano := uint64(1_700_000_000_000_000)
	endNano := uint64(1_700_000_005_000_000)

	otlpReq := buildOTLPRequest("svc", "op", tracepb.Span_SPAN_KIND_SERVER, nil, startNano, endNano)
	body, _ := proto.Marshal(otlpReq)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	records := q.Drain()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	expectedStart := int64(1_700_000_000_000)
	expectedDuration := int64(5_000)

	if records[0].StartTimeUs != expectedStart {
		t.Fatalf("expected startTimeUs %d, got %d", expectedStart, records[0].StartTimeUs)
	}
	if records[0].DurationUs != expectedDuration {
		t.Fatalf("expected durationUs %d, got %d", expectedDuration, records[0].DurationUs)
	}
}

func TestOTLPNoAPIKey(t *testing.T) {
	ts, _ := setupOTLPTestServer(t)

	otlpReq := buildOTLPRequest("svc", "op", tracepb.Span_SPAN_KIND_SERVER, nil, 1000000000, 2000000000)
	body, _ := proto.Marshal(otlpReq)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	// No auth header

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestOTLPBearerAuth(t *testing.T) {
	ts, q := setupOTLPTestServer(t)

	otlpReq := buildOTLPRequest("bearer-svc", "op", tracepb.Span_SPAN_KIND_SERVER, nil, 1000000000, 2000000000)
	body, _ := proto.Marshal(otlpReq)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	records := q.Drain()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Service != "bearer-svc" {
		t.Fatalf("expected service 'bearer-svc', got %q", records[0].Service)
	}
}
