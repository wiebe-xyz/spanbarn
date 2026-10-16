package model

import "encoding/json"

// SpanRecord is the intermediate representation between HTTP ingest and storage.
type SpanRecord struct {
	ProjectID    int64           `json:"project_id"`
	TraceID      string          `json:"trace_id"`
	SpanID       string          `json:"span_id"`
	ParentSpanID string          `json:"parent_span_id,omitempty"`
	Name         string          `json:"name"`
	Service      string          `json:"service"`
	Resource     string          `json:"resource,omitempty"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	StartTimeUs  int64           `json:"start_time_us"`
	DurationUs   int64           `json:"duration_us"`
	Attributes   json.RawMessage `json:"attributes,omitempty"`
	Events       json.RawMessage `json:"events,omitempty"`
	// SampleWeight is how many traces of the population this span's trace
	// stands for, set by the trace buffer when it keeps the trace. 0 means
	// unknown: the span reached storage without a sampling decision.
	SampleWeight int `json:"sample_weight,omitempty"`
}
