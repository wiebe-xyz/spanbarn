package api

import "encoding/json"

// SpanBatch is the top-level request body for POST /api/v1/spans.
type SpanBatch struct {
	Spans []SpanInput `json:"spans"`
}

// SpanInput represents a single span in the ingest payload.
// Accepts both camelCase (traceId) and snake_case (trace_id) field names.
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

func (s *SpanInput) UnmarshalJSON(data []byte) error {
	type alias SpanInput
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*s = SpanInput(a)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	jsonStr := func(key string) string {
		v, ok := raw[key]
		if !ok {
			return ""
		}
		var str string
		json.Unmarshal(v, &str)
		return str
	}
	jsonInt := func(key string) int64 {
		v, ok := raw[key]
		if !ok {
			return 0
		}
		var n int64
		json.Unmarshal(v, &n)
		return n
	}

	if s.TraceID == "" {
		s.TraceID = jsonStr("trace_id")
	}
	if s.SpanID == "" {
		s.SpanID = jsonStr("span_id")
	}
	if s.ParentSpanID == "" {
		s.ParentSpanID = jsonStr("parent_span_id")
	}
	if s.StartTime == 0 {
		s.StartTime = jsonInt("start_time_us")
	}
	if s.Duration == 0 {
		s.Duration = jsonInt("duration_us")
	}
	return nil
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
