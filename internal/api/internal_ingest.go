package api

import (
	"encoding/json"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// handleInternalIngest receives pre-validated SpanRecords forwarded by ingest pods.
func (s *Server) handleInternalIngest(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.internal_ingest.receive", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	var records []model.SpanRecord
	if err := json.NewDecoder(r.Body).Decode(&records); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
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
