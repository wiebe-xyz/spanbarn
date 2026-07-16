package api

import (
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/observability"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

var apiTracer = otel.Tracer("spanbarn/api")

func (s *Server) handleOTLP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	selfExport := observability.IsSelfInstrument(ctx)

	var span trace.Span
	if !selfExport {
		ctx, span = apiTracer.Start(ctx, "api.otlp.receive", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	body, ok := readOTLPBody(w, r)
	if !ok {
		return
	}

	var req collectorpb.ExportTraceServiceRequest
	if !decodeOTLP(w, r, body, &req) {
		return
	}

	projectID := GetProjectID(r.Context())

	// Guardrail: external OTLP arriving on the global/admin key authenticates as
	// projectID 0, so its spans are stamped project 0 and stay invisible in every
	// per-project view. In single-tenant deployments project 0 is the legitimate
	// default, and self-instrument spans ride the internal path with projectID 0
	// by design — so we warn (throttled) rather than reject, surfacing a likely
	// misconfigured client without dropping data or breaking single-tenant setups.
	if !selfExport && projectID == 0 {
		warnOrphanedIngest(s.logger, extractRequestService(&req))
	}

	records := otlpToSpanRecords(&req, projectID)

	if span != nil {
		span.SetAttributes(attribute.Int("span_count", len(records)))
	}

	if s.traceBuffer != nil {
		// Tail-based sampling: add to the trace buffer and let it decide
		// per-trace after the configured TTL. Error traces always pass.
		//
		// Self-instrument spans go through the buffer like any other project.
		// They used to bypass it so the pod's own traces "always reached the
		// writer", but self-telemetry is by far the largest span producer, so
		// exempting it made its sample ratio unenforceable and let it fill the
		// disk. keep() still passes every error trace, so the traces that
		// matter are unaffected.
		for _, rec := range records {
			s.traceBuffer.Add(rec)
		}
	} else {
		_, enqueueSpan := apiTracer.Start(ctx, "api.otlp.enqueue")
		for _, rec := range records {
			s.ingest.Enqueue(rec)
		}
		enqueueSpan.End()
	}

	writeOTLPResponse(w, r, &collectorpb.ExportTraceServiceResponse{})
}

// otlpToSpanRecords converts an OTLP ExportTraceServiceRequest into SpanRecords
// stamped with the given projectID (from the authenticated API key).
func otlpToSpanRecords(req *collectorpb.ExportTraceServiceRequest, projectID int64) []model.SpanRecord {
	var records []model.SpanRecord
	for _, rs := range req.GetResourceSpans() {
		serviceName := extractServiceName(rs.GetResource().GetAttributes())
		resourceAttrs := kvListToMap(rs.GetResource().GetAttributes())

		for _, ss := range rs.GetScopeSpans() {
			scopeAttrs := kvListToMap(ss.GetScope().GetAttributes())

			for _, span := range ss.GetSpans() {
				spanAttrs := kvListToMap(span.GetAttributes())

				// Merge attributes: resource < scope < span (span wins).
				merged := make(map[string]any)
				for k, v := range resourceAttrs {
					merged[k] = v
				}
				for k, v := range scopeAttrs {
					merged[k] = v
				}
				for k, v := range spanAttrs {
					merged[k] = v
				}

				var attrsJSON json.RawMessage
				if len(merged) > 0 {
					attrsJSON, _ = json.Marshal(merged)
				}

				var eventsJSON json.RawMessage
				if len(span.GetEvents()) > 0 {
					eventsJSON = marshalSpanEvents(span.GetEvents())
				}

				rec := model.SpanRecord{
					ProjectID:    projectID,
					TraceID:      hex.EncodeToString(span.GetTraceId()),
					SpanID:       hex.EncodeToString(span.GetSpanId()),
					ParentSpanID: hexIfNonEmpty(span.GetParentSpanId()),
					Name:         span.GetName(),
					Service:      serviceName,
					Kind:         mapSpanKind(span.GetKind()),
					Status:       mapStatusCode(span.GetStatus()),
					StartTimeUs:  int64(span.GetStartTimeUnixNano() / 1000),
					DurationUs:   int64((span.GetEndTimeUnixNano() - span.GetStartTimeUnixNano()) / 1000),
					Attributes:   attrsJSON,
					Events:       eventsJSON,
				}

				// Extract resource from merged attributes.
				if len(merged) > 0 {
					raw, _ := json.Marshal(merged)
					rec.Resource = extractResource(raw)
				}

				collapseSpanParams(&rec)

				records = append(records, rec)
			}
		}
	}
	return records
}

// lastOrphanWarnMinute throttles the orphaned-ingest warning to at most once per
// wall-clock minute per process, since OTLP ingest is a hot path.
var lastOrphanWarnMinute atomic.Int64

// warnOrphanedIngest logs (throttled) that external OTLP spans are being stamped
// project 0 because the client authenticated with the global/admin key.
func warnOrphanedIngest(logger *slog.Logger, service string) {
	if logger == nil {
		return
	}
	minute := time.Now().Unix() / 60
	prev := lastOrphanWarnMinute.Load()
	if prev >= minute || !lastOrphanWarnMinute.CompareAndSwap(prev, minute) {
		return
	}
	logger.Warn("OTLP ingest on global/admin key is stamped project 0 and hidden from project views; configure a project-scoped API key",
		"service", service)
}

// extractRequestService returns the service.name of the first resource in an
// OTLP request, for diagnostics. Empty when none is present.
func extractRequestService(req *collectorpb.ExportTraceServiceRequest) string {
	for _, rs := range req.GetResourceSpans() {
		if name := extractServiceName(rs.GetResource().GetAttributes()); name != "" && name != "unknown" {
			return name
		}
	}
	return ""
}

// extractServiceName finds service.name in OTLP resource attributes.
func extractServiceName(attrs []*commonpb.KeyValue) string {
	for _, kv := range attrs {
		if kv.GetKey() == "service.name" {
			if sv := kv.GetValue().GetStringValue(); sv != "" {
				return sv
			}
		}
	}
	return "unknown"
}

// kvListToMap converts OTLP KeyValue list to a Go map.
func kvListToMap(attrs []*commonpb.KeyValue) map[string]any {
	if len(attrs) == 0 {
		return nil
	}
	m := make(map[string]any, len(attrs))
	for _, kv := range attrs {
		m[kv.GetKey()] = anyValueToGo(kv.GetValue())
	}
	return m
}

// anyValueToGo converts an OTLP AnyValue to a Go value.
func anyValueToGo(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch val := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return val.StringValue
	case *commonpb.AnyValue_IntValue:
		return val.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return val.DoubleValue
	case *commonpb.AnyValue_BoolValue:
		return val.BoolValue
	case *commonpb.AnyValue_ArrayValue:
		var arr []any
		for _, elem := range val.ArrayValue.GetValues() {
			arr = append(arr, anyValueToGo(elem))
		}
		return arr
	case *commonpb.AnyValue_KvlistValue:
		return kvListToMap(val.KvlistValue.GetValues())
	case *commonpb.AnyValue_BytesValue:
		return hex.EncodeToString(val.BytesValue)
	default:
		return nil
	}
}

// mapSpanKind maps OTLP SpanKind enum to string.
func mapSpanKind(kind tracepb.Span_SpanKind) string {
	switch kind {
	case tracepb.Span_SPAN_KIND_SERVER:
		return "server"
	case tracepb.Span_SPAN_KIND_CLIENT:
		return "client"
	case tracepb.Span_SPAN_KIND_PRODUCER:
		return "producer"
	case tracepb.Span_SPAN_KIND_CONSUMER:
		return "consumer"
	case tracepb.Span_SPAN_KIND_INTERNAL:
		return "internal"
	default:
		return "internal"
	}
}

// mapStatusCode maps OTLP StatusCode to string.
func mapStatusCode(status *tracepb.Status) string {
	if status == nil {
		return "unset"
	}
	switch status.GetCode() {
	case tracepb.Status_STATUS_CODE_OK:
		return "ok"
	case tracepb.Status_STATUS_CODE_ERROR:
		return "error"
	default:
		return "unset"
	}
}

// hexIfNonEmpty converts bytes to hex string, returns empty string for nil/empty.
func hexIfNonEmpty(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

// marshalSpanEvents converts OTLP span events to a JSON array.
func marshalSpanEvents(events []*tracepb.Span_Event) json.RawMessage {
	type eventJSON struct {
		Name       string         `json:"name"`
		Time       uint64         `json:"time"`
		Attributes map[string]any `json:"attributes,omitempty"`
	}
	var out []eventJSON
	for _, e := range events {
		ev := eventJSON{
			Name: e.GetName(),
			Time: e.GetTimeUnixNano(),
		}
		if attrs := kvListToMap(e.GetAttributes()); len(attrs) > 0 {
			ev.Attributes = attrs
		}
		out = append(out, ev)
	}
	data, _ := json.Marshal(out)
	return data
}
