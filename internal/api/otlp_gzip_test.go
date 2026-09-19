package api_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/spool"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// setupBoundedOTLPServer is setupOTLPTestServer with a caller-chosen body limit.
func setupBoundedOTLPServer(t *testing.T, maxBody int64) (*httptest.Server, *ingest.Queue) {
	t.Helper()
	q := ingest.NewQueue(1024)
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })
	h := ingest.NewHandler(q, sp, 0, slog.Default())
	srv := api.NewServer(api.ServerConfig{APIKey: testAPIKey, MaxBodyBytes: maxBody, Version: "test"}, h, slog.Default())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, q
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func postOTLP(t *testing.T, ts *httptest.Server, path, contentType, encoding string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func requireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: want %d, got %d: %s", want, resp.StatusCode, body)
	}
}

func spanRequest(t *testing.T) *collectortracepb.ExportTraceServiceRequest {
	t.Helper()
	return buildOTLPRequest("gzip-service", "GET /gzip", tracepb.Span_SPAN_KIND_SERVER, nil, 1700000000000000, 1700000005000000)
}

func TestOTLPGzipProtobuf(t *testing.T) {
	ts, q := setupOTLPTestServer(t)
	raw, err := proto.Marshal(spanRequest(t))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	requireStatus(t, postOTLP(t, ts, "/v1/traces", "application/x-protobuf", "gzip", gzipBytes(t, raw)), http.StatusOK)

	records := q.Drain()
	if len(records) != 1 || records[0].Name != "GET /gzip" {
		t.Fatalf("want one record named 'GET /gzip', got %+v", records)
	}
}

func TestOTLPGzipJSON(t *testing.T) {
	ts, q := setupOTLPTestServer(t)
	raw, err := protojson.Marshal(spanRequest(t))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	requireStatus(t, postOTLP(t, ts, "/v1/traces", "application/json", "GZIP", gzipBytes(t, raw)), http.StatusOK)

	if records := q.Drain(); len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
}

func TestOTLPIdentityEncodingAccepted(t *testing.T) {
	ts, q := setupOTLPTestServer(t)
	raw, _ := proto.Marshal(spanRequest(t))

	requireStatus(t, postOTLP(t, ts, "/v1/traces", "application/x-protobuf", "identity", raw), http.StatusOK)

	if records := q.Drain(); len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
}

func TestOTLPGzipHeaderOnPlainBodyIs400(t *testing.T) {
	ts, _ := setupOTLPTestServer(t)
	raw, _ := proto.Marshal(spanRequest(t))

	requireStatus(t, postOTLP(t, ts, "/v1/traces", "application/x-protobuf", "gzip", raw), http.StatusBadRequest)
}

func TestOTLPGzipEmptyBodyIs400(t *testing.T) {
	ts, _ := setupOTLPTestServer(t)

	requireStatus(t, postOTLP(t, ts, "/v1/traces", "application/x-protobuf", "gzip", nil), http.StatusBadRequest)
}

func TestOTLPUnsupportedEncodingIs415(t *testing.T) {
	ts, _ := setupOTLPTestServer(t)
	raw, _ := proto.Marshal(spanRequest(t))

	requireStatus(t, postOTLP(t, ts, "/v1/traces", "application/x-protobuf", "br", raw), http.StatusUnsupportedMediaType)
}

func TestOTLPPlainBodyOverLimitIs413(t *testing.T) {
	ts, _ := setupBoundedOTLPServer(t, 256)
	body := bytes.Repeat([]byte{0}, 1024)

	requireStatus(t, postOTLP(t, ts, "/v1/traces", "application/x-protobuf", "", body), http.StatusRequestEntityTooLarge)
}

// A gzip bomb passes the wire cap because it compresses well, so the cap has to
// hold on the decompressed stream too.
func TestOTLPGzipInflatingPastLimitIs413(t *testing.T) {
	ts, _ := setupBoundedOTLPServer(t, 4096)
	inflated := bytes.Repeat([]byte{0}, 1<<20)
	compressed := gzipBytes(t, inflated)
	if len(compressed) >= 4096 {
		t.Fatalf("test needs a body that fits the wire cap, got %d compressed bytes", len(compressed))
	}

	requireStatus(t, postOTLP(t, ts, "/v1/traces", "application/x-protobuf", "gzip", compressed), http.StatusRequestEntityTooLarge)
}

func TestOTLPMetricsGzip(t *testing.T) {
	ts, repo := setupMetricsTestServer(t)
	raw, err := proto.Marshal(buildGaugeRequest("gzip.gauge", 1.5))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	requireStatus(t, postOTLP(t, ts, "/v1/metrics", "application/x-protobuf", "gzip", gzipBytes(t, raw)), http.StatusOK)

	if recs := waitForRecords(t, repo, 1); len(recs) != 1 || recs[0].Name != "gzip.gauge" {
		t.Fatalf("want one metric named gzip.gauge, got %+v", recs)
	}
}

func TestOTLPLogsGzip(t *testing.T) {
	ts, repo := setupLogsTestServer(t)
	raw, err := proto.Marshal(buildLogRequest(nil, "gzip log line", "gzip-svc", 9))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	requireStatus(t, postOTLP(t, ts, "/v1/logs", "application/x-protobuf", "gzip", gzipBytes(t, raw)), http.StatusOK)

	if recs := waitForLogRecords(t, repo, 1); len(recs) != 1 {
		t.Fatalf("want 1 log record, got %d", len(recs))
	}
}

func TestGRPCGzipExport(t *testing.T) {
	traceSvc, _, _, q := setupGRPCTest(t)

	// The literal name, with no import of grpc/encoding/gzip in this file: the
	// compressor registry is process-global, so importing it here would make the
	// test pass even if the API package never registered gzip itself.
	_, err := traceSvc.Export(authCtx(testAPIKey), spanRequest(t), grpc.UseCompressor("gzip"))
	if err != nil {
		t.Fatalf("gzip Export: %v", err)
	}
	if records := q.Drain(); len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
}

func TestGRPCMessageOverLimitIsResourceExhausted(t *testing.T) {
	traceSvc, q := setupBoundedGRPC(t, 1024)
	req := spanRequest(t)
	req.ResourceSpans[0].ScopeSpans[0].Spans[0].Name = strings.Repeat("x", 4096)

	_, err := traceSvc.Export(authCtx(testAPIKey), req)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("want ResourceExhausted, got %v", err)
	}
	if records := q.Drain(); len(records) != 0 {
		t.Fatalf("want no records, got %d", len(records))
	}
}

// setupBoundedGRPC builds a gRPC trace client against a server whose body limit
// is maxBody, so the limit shared with the HTTP transport can be exercised.
func setupBoundedGRPC(t *testing.T, maxBody int64) (collectortracepb.TraceServiceClient, *ingest.Queue) {
	t.Helper()
	q := ingest.NewQueue(1024)
	sp, err := spool.NewSpool(t.TempDir(), spool.DefaultMaxBytes)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })
	h := ingest.NewHandler(q, sp, 0, slog.Default())
	srv := api.NewServer(api.ServerConfig{APIKey: testAPIKey, MaxBodyBytes: maxBody, Version: "test"}, h, slog.Default())

	conn := dialBufGRPC(t, api.NewGRPCServer(srv, slog.Default()))
	return collectortracepb.NewTraceServiceClient(conn), q
}

func dialBufGRPC(t *testing.T, g *api.GRPCServer) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	t.Cleanup(g.Stop)
	go func() { _ = g.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
