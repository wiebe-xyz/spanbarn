package api_test

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"testing"

	"google.golang.org/protobuf/proto"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func scrapeMetrics(t *testing.T, baseURL string) string {
	t.Helper()
	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape: status %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

// TestSpansIngestedCountsOTLPSpans goes through the real handler: the counter
// used to be registered and never incremented, so it read 0 in production while
// ingest ran at several requests a second.
func TestSpansIngestedCountsOTLPSpans(t *testing.T) {
	ts, q := setupOTLPTestServer(t)

	req := buildOTLPRequest("svc", "GET /a", tracepb.Span_SPAN_KIND_SERVER, nil, 1700000000000000, 1700000005000000)
	second := proto.Clone(req.ResourceSpans[0].ScopeSpans[0].Spans[0]).(*tracepb.Span)
	second.SpanId = []byte{0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01}
	req.ResourceSpans[0].ScopeSpans[0].Spans = append(req.ResourceSpans[0].ScopeSpans[0].Spans, second)

	for i := 0; i < 2; i++ {
		postOTLPProto(t, ts.URL, req)
	}

	if got := len(q.Drain()); got != 4 {
		t.Fatalf("queued %d spans, want 4", got)
	}
	body := scrapeMetrics(t, ts.URL)
	if !regexp.MustCompile(`(?m)^spans_ingested_total 4$`).MatchString(body) {
		t.Errorf("spans_ingested_total is not 4 after ingesting 4 spans:\n%s", grepLines(body, "spans_"))
	}
	// This server runs no worker, so the worker counters must be absent rather
	// than a misleading zero.
	if regexp.MustCompile(`(?m)^spans_processed_total `).MatchString(body) {
		t.Error("spans_processed_total exported on a server with no worker")
	}
}

func postOTLPProto(t *testing.T, baseURL string, req *collectorpb.ExportTraceServiceRequest) {
	t.Helper()
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	httpReq, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/traces", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("X-SpanBarn-Api-Key", testAPIKey)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("post: status %d: %s", resp.StatusCode, b)
	}
}

func grepLines(body, substr string) string {
	var out []byte
	for _, line := range bytes.Split([]byte(body), []byte("\n")) {
		if bytes.Contains(line, []byte(substr)) {
			out = append(append(out, line...), '\n')
		}
	}
	return string(out)
}
