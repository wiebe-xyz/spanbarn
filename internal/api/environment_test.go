package api

import (
	"encoding/json"
	"testing"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func envOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("attributes %s: %v", raw, err)
	}
	s, _ := m["deployment.environment"].(string)
	return s
}

func traceReq(attrs ...*commonpb.KeyValue) *collectorpb.ExportTraceServiceRequest {
	return &collectorpb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: append([]*commonpb.KeyValue{strAttr("service.name", "web")}, attrs...)},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: make([]byte, 16), SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Name: "GET /",
			StartTimeUnixNano: 1, EndTimeUnixNano: 2,
		}}}},
	}}}
}

// A key's environment fills a span whose resource names none, and never
// overrides one it does name (#558).
func TestStampTraceEnvironmentPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		attrs  []*commonpb.KeyValue
		keyEnv string
		want   string
	}{
		{"key environment fills an empty resource", nil, "staging", "staging"},
		{"own deployment.environment wins over the key", []*commonpb.KeyValue{strAttr(envAttr, "production")}, "staging", "production"},
		{"deployment.environment.name is read", []*commonpb.KeyValue{strAttr(envAttrName, "testing")}, "", "testing"},
		{"the .name attribute beats the key", []*commonpb.KeyValue{strAttr(envAttrName, "testing")}, "staging", "testing"},
		{"nothing to stamp stays empty", nil, "", ""},
		{"an empty own value falls back to the key", []*commonpb.KeyValue{strAttr(envAttr, "")}, "staging", "staging"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := traceReq(tc.attrs...)
			stampTraceEnvironment(req, tc.keyEnv)
			recs := otlpToSpanRecords(req, 7)
			if len(recs) != 1 {
				t.Fatalf("records = %d, want 1", len(recs))
			}
			if got := envOf(t, recs[0].Attributes); got != tc.want {
				t.Errorf("deployment.environment = %q, want %q", got, tc.want)
			}
		})
	}
}

// A request whose resource is missing still takes the key's environment.
func TestStampTraceEnvironmentNilResource(t *testing.T) {
	req := traceReq()
	req.ResourceSpans[0].Resource = nil
	stampTraceEnvironment(req, "staging")
	recs := otlpToSpanRecords(req, 7)
	if got := envOf(t, recs[0].Attributes); got != "staging" {
		t.Errorf("deployment.environment = %q, want staging", got)
	}
}

// Logs and metrics take the same fallback as spans.
func TestStampLogAndMetricEnvironment(t *testing.T) {
	logReq := &collectorlogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", "web")}},
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{TimeUnixNano: 1, Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "hi"}}}}}},
	}}}
	stampLogEnvironment(logReq, "staging")
	logs := otlpToLogRecords(logReq, 7)
	if len(logs) != 1 || envOf(t, logs[0].Attributes) != "staging" {
		t.Errorf("log records = %+v, want one stamped staging", logs)
	}

	metricReq := &collectormetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", "web"), strAttr(envAttrName, "testing")}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
			Name: "requests",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
				TimeUnixNano: 1, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 3},
			}}}},
		}}}},
	}}}
	stampMetricEnvironment(metricReq, "staging")
	metrics := otlpToMetricRecords(metricReq, 7)
	if len(metrics) != 1 || envOf(t, metrics[0].Attributes) != "testing" {
		t.Errorf("metric records = %+v, want one stamped testing", metrics)
	}
}
