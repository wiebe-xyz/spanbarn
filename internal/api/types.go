package api

import "encoding/json"

// SpanBatch is the top-level request body for POST /api/v1/spans.
type SpanBatch struct {
	Spans []SpanInput `json:"spans"`
}

// SpanInput represents a single span in the ingest payload.
type SpanInput struct {
	TraceID      string          `json:"traceId"`
	SpanID       string          `json:"spanId"`
	ParentSpanID string          `json:"parentSpanId,omitempty"`
	Name         string          `json:"name"`
	Service      string          `json:"service"`
	Resource     string          `json:"resource,omitempty"`
	Kind         string          `json:"kind,omitempty"`
	Status       string          `json:"status,omitempty"`
	StartTime    int64           `json:"startTime"`
	Duration     int64           `json:"duration"`
	Attributes   json.RawMessage `json:"attributes,omitempty"`
	Events       json.RawMessage `json:"events,omitempty"`
}

// IngestResponse is returned on successful span ingestion.
type IngestResponse struct {
	Accepted int `json:"accepted"`
}

// HealthResponse is returned by the health endpoint.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// ErrorResponse is returned for client/server errors.
type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}
