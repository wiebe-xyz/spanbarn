package api

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/observability"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large", "")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read body", err.Error())
		return
	}

	var req collectorpb.ExportTraceServiceRequest
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "application/json"):
		if err := protojson.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
			return
		}
	default:
		if err := proto.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid protobuf", err.Error())
			return
		}
	}

	projectID := GetProjectID(r.Context())
	records := otlpToSpanRecords(&req, projectID)

	if span != nil {
		span.SetAttributes(attribute.Int("span_count", len(records)))
	}

	if selfExport {
		// Self-instrument spans bypass the TraceBuffer so they are never
		// dropped by project-level sampling — the pod's own traces must
		// always reach the writer.
		for _, rec := range records {
			s.ingest.Enqueue(rec)
		}
	} else if s.traceBuffer != nil {
		// Tail-based sampling: add to the trace buffer and let it decide
		// per-trace after the configured TTL. Error traces always pass.
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

	// Return ExportTraceServiceResponse.
	resp := &collectorpb.ExportTraceServiceResponse{}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		data, _ := protojson.Marshal(resp)
		_, _ = w.Write(data)
	} else {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		data, _ := proto.Marshal(resp)
		_, _ = w.Write(data)
	}
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

				records = append(records, rec)
			}
		}
	}
	return records
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
