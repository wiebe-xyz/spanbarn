package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.ingest.receive", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	var batch SpanBatch
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&batch); err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large", "")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
		return
	}

	// Validate and convert each span.
	var errs []string
	records := make([]model.SpanRecord, 0, len(batch.Spans))
	for i, sp := range batch.Spans {
		if err := validateSpan(sp); err != nil {
			errs = append(errs, fmt.Sprintf("span[%d]: %s", i, err.Error()))
			continue
		}

		rec := model.SpanRecord{
			TraceID:      sp.TraceID,
			SpanID:       sp.SpanID,
			ParentSpanID: sp.ParentSpanID,
			Name:         sp.Name,
			Service:      sp.Service,
			Resource:     sp.Resource,
			Kind:         normalizeKind(sp.Kind),
			Status:       normalizeStatus(sp.Status),
			StartTimeUs:  sp.StartTime,
			DurationUs:   sp.Duration,
			Attributes:   sp.Attributes,
			Events:       sp.Events,
		}

		// Extract resource from attributes if not provided.
		if rec.Resource == "" && len(sp.Attributes) > 0 {
			rec.Resource = extractResource(sp.Attributes)
		}

		records = append(records, rec)
	}

	if len(errs) > 0 {
		writeError(w, http.StatusBadRequest, "validation failed", strings.Join(errs, "; "))
		return
	}

	span.SetAttributes(attribute.Int("span_count", len(records)))

	for _, rec := range records {
		s.ingest.Enqueue(rec)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(IngestResponse{Accepted: len(records)})
}

func validateSpan(sp SpanInput) error {
	var missing []string
	if sp.TraceID == "" {
		missing = append(missing, "traceId")
	}
	if sp.SpanID == "" {
		missing = append(missing, "spanId")
	}
	if sp.Name == "" {
		missing = append(missing, "name")
	}
	if sp.Service == "" {
		missing = append(missing, "service")
	}
	if sp.StartTime == 0 {
		missing = append(missing, "startTime")
	}
	if sp.Duration < 0 {
		missing = append(missing, "duration")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

func normalizeKind(kind string) string {
	if kind == "" {
		return "internal"
	}
	return kind
}

func normalizeStatus(status string) string {
	if status == "" {
		return "unset"
	}
	return status
}

// extractResource tries to derive a resource name from span attributes.
// It checks common attribute keys in priority order.
func extractResource(raw json.RawMessage) string {
	var attrs map[string]any
	if err := json.Unmarshal(raw, &attrs); err != nil {
		return ""
	}
	keys := []string{"http.route", "db.statement", "rpc.method", "aws.operation"}
	for _, k := range keys {
		if v, ok := attrs[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func writeError(w http.ResponseWriter, status int, msg, details string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := ErrorResponse{Error: msg}
	if details != "" {
		resp.Details = details
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func writeServerError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	attrs := []any{"error", err, "method", r.Method, "path", r.URL.Path}
	if id := getRequestID(r); id != "" {
		attrs = append(attrs, "request_id", id)
	}
	slog.Error(msg, attrs...)
	writeError(w, http.StatusInternalServerError, msg, "")
}
