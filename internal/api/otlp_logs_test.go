package api_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/model"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type captureLogsRepo struct {
	mu      sync.Mutex
	records []model.LogRecord
}

func (r *captureLogsRepo) InsertLogs(_ context.Context, recs []model.LogRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, recs...)
	return nil
}

func (r *captureLogsRepo) all() []model.LogRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.LogRecord{}, r.records...)
}

func setupLogsTestServer(t *testing.T) (*httptest.Server, *captureLogsRepo) {
	t.Helper()
	repo := &captureLogsRepo{}
	lh := ingest.NewLogsHandler(repo, slog.Default())

	srv := api.NewServerWithQuery(api.ServerConfig{
		APIKey:       testAPIKey,
		MaxBodyBytes: 4 << 20,
		Version:      "test",
	}, nil, nil, nil, slog.Default(), api.WithLogsHandler(lh))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go lh.Run(ctx)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, repo
}

func buildLogRequest(traceID []byte, body, service string, severity int32) *collectorlogspb.ExportLogsServiceRequest {
	return &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: service}}},
					},
				},
				ScopeLogs: []*logspb.ScopeLogs{
					{
						LogRecords: []*logspb.LogRecord{
							{
								TimeUnixNano:   1700000000000000000,
								SeverityNumber: logspb.SeverityNumber(severity),
								SeverityText:   "ERROR",
								Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}},
								TraceId:        traceID,
							},
						},
					},
				},
			},
		},
	}
}

func postLogs(t *testing.T, ts *httptest.Server, req *collectorlogspb.ExportLogsServiceRequest, contentType string) *http.Response {
	t.Helper()
	var body []byte
	if contentType == "application/json" {
		var err error
		body, err = protojson.Marshal(req)
		if err != nil {
			t.Fatalf("protojson.Marshal: %v", err)
		}
	} else {
		var err error
		body, err = proto.Marshal(req)
		if err != nil {
			t.Fatalf("proto.Marshal: %v", err)
		}
		contentType = "application/x-protobuf"
	}
	httpReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/logs", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("X-SpanBarn-Api-Key", testAPIKey)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("POST /v1/logs: %v", err)
	}
	return resp
}

func waitForLogRecords(t *testing.T, repo *captureLogsRepo, want int) []model.LogRecord {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if recs := repo.all(); len(recs) >= want {
			return recs
		}
		time.Sleep(10 * time.Millisecond)
	}
	return repo.all()
}

func TestOTLPLogsProtobuf(t *testing.T) {
	ts, repo := setupLogsTestServer(t)
	traceID, _ := hex.DecodeString("aabbccddeeff00112233445566778899")
	resp := postLogs(t, ts, buildLogRequest(traceID, "something broke", "my-svc", 17), "application/x-protobuf")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	recs := waitForLogRecords(t, repo, 1)
	if len(recs) == 0 {
		t.Fatal("want at least 1 record, got 0")
	}
	if recs[0].Body != "something broke" {
		t.Errorf("body: want 'something broke', got %q", recs[0].Body)
	}
	if recs[0].TraceID != "aabbccddeeff00112233445566778899" {
		t.Errorf("traceID: want hex, got %q", recs[0].TraceID)
	}
	if recs[0].SeverityNumber != 17 {
		t.Errorf("severity: want 17, got %d", recs[0].SeverityNumber)
	}
}

func TestOTLPLogsJSON(t *testing.T) {
	ts, repo := setupLogsTestServer(t)
	resp := postLogs(t, ts, buildLogRequest(nil, "no trace log", "my-svc", 9), "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	recs := waitForLogRecords(t, repo, 1)
	if len(recs) == 0 {
		t.Fatal("want at least 1 record, got 0")
	}
	if recs[0].TraceID != "" {
		t.Errorf("traceID should be empty for nil traceID, got %q", recs[0].TraceID)
	}
}

func TestOTLPLogsNoTraceID(t *testing.T) {
	ts, repo := setupLogsTestServer(t)
	resp := postLogs(t, ts, buildLogRequest(nil, "orphan log", "svc", 9), "application/x-protobuf")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	recs := waitForLogRecords(t, repo, 1)
	if len(recs) == 0 {
		t.Fatal("want at least 1 record")
	}
	if recs[0].TraceID != "" {
		t.Errorf("want empty traceID, got %q", recs[0].TraceID)
	}
}

func TestOTLPLogsNoAPIKey(t *testing.T) {
	ts, _ := setupLogsTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/logs", bytes.NewReader([]byte{}))
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", resp.StatusCode)
	}
}

func TestOTLPLogsMethodNotAllowed(t *testing.T) {
	ts, _ := setupLogsTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/logs", nil)
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", resp.StatusCode)
	}
}

func TestOTLPLogsAttributeMerge(t *testing.T) {
	ts, repo := setupLogsTestServer(t)

	req := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "checkout"}}},
					{Key: "env", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "prod"}}},
				},
			},
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano:   1700000000000000000,
					SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
					Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "order placed"}},
					Attributes: []*commonpb.KeyValue{
						{Key: "order.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "ord-123"}}},
					},
				}},
			}},
		}},
	}

	resp := postLogs(t, ts, req, "application/x-protobuf")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	recs := waitForLogRecords(t, repo, 1)
	if len(recs) == 0 {
		t.Fatal("want 1 record")
	}
	// Attributes JSON should contain merged resource + log-record attrs.
	attrs := string(recs[0].Attributes)
	if attrs == "" || attrs == "{}" {
		t.Error("attributes should not be empty")
	}
}
