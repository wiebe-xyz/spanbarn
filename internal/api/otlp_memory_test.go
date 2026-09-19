package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func strKV(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// realisticBatch builds an OTLP export of about target bytes: a Kubernetes-style
// resource of 16 attributes and spans of 10 attributes each. The resource is
// copied into every span record, which is what makes a request's heap cost a
// multiple of its wire size.
func realisticBatch(target int) *collectorpb.ExportTraceServiceRequest {
	res := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strKV("service.name", "checkout")}}
	for i := 0; i < 15; i++ {
		res.Attributes = append(res.Attributes, strKV(fmt.Sprintf("k8s.resource.attr.%02d", i), "value-of-a-typical-kubernetes-attribute"))
	}
	req := &collectorpb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{Resource: res}}}
	ss := &tracepb.ScopeSpans{}
	req.ResourceSpans[0].ScopeSpans = []*tracepb.ScopeSpans{ss}

	for i := 0; proto.Size(req) < target; i++ {
		span := &tracepb.Span{
			TraceId:           bytes.Repeat([]byte{byte(i)}, 16),
			SpanId:            bytes.Repeat([]byte{byte(i >> 8)}, 8),
			Name:              "GET /api/v1/orders/{id}",
			StartTimeUnixNano: 1700000000000000000,
			EndTimeUnixNano:   1700000000050000000,
		}
		for j := 0; j < 10; j++ {
			span.Attributes = append(span.Attributes, strKV(fmt.Sprintf("http.attr.%d", j), "a-representative-attribute-value"))
		}
		ss.Spans = append(ss.Spans, span)
	}
	return req
}

// ingestOnce runs the request path up to the point where records are handed to
// the queue, and returns everything that is live at that moment.
func ingestOnce(s *Server, body []byte) (raw []byte, req *collectorpb.ExportTraceServiceRequest, records []model.SpanRecord) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/x-protobuf")
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)

	raw, ok := s.readOTLPBody(w, r)
	if !ok {
		panic("read failed")
	}
	req = &collectorpb.ExportTraceServiceRequest{}
	if !decodeOTLP(w, r, raw, req) {
		panic("decode failed")
	}
	return raw, req, otlpToSpanRecords(req, 1)
}

// BenchmarkOTLPIngestMemory reports what one maximum-size request costs the
// heap, which is what sizes SPANBARN_MAX_BODY_BYTES against the ingest pod's
// GOMEMLIMIT. Run with:
//
//	go test ./internal/api -run '^$' -bench OTLPIngestMemory -benchmem
func BenchmarkOTLPIngestMemory(b *testing.B) {
	const limit = 4 << 20
	body, err := proto.Marshal(realisticBatch(limit - 64<<10))
	if err != nil {
		b.Fatal(err)
	}
	s := &Server{maxBodyBytes: limit}

	var before, after, live runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	raw, req, records := ingestOnce(s, body)
	runtime.ReadMemStats(&after)
	runtime.GC()
	runtime.ReadMemStats(&live)
	runtime.KeepAlive(raw)
	runtime.KeepAlive(req)
	runtime.KeepAlive(records)
	b.Logf("body %d bytes, %d spans: %.1f MiB allocated by one request (%.1fx the body), %.1f MiB live at the hand-off to the queue",
		len(body), len(records), float64(after.TotalAlloc-before.TotalAlloc)/(1<<20),
		float64(after.TotalAlloc-before.TotalAlloc)/float64(len(body)),
		float64(live.HeapAlloc-before.HeapAlloc)/(1<<20))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ingestOnce(s, body)
	}
}
