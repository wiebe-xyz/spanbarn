package spanbarn

// SpanData represents a completed span ready for export.
type SpanData struct {
	TraceID      string                 `json:"traceId"`
	SpanID       string                 `json:"spanId"`
	ParentSpanID string                 `json:"parentSpanId,omitempty"`
	Name         string                 `json:"name"`
	Service      string                 `json:"service"`
	Resource     string                 `json:"resource,omitempty"`
	Kind         string                 `json:"kind"`
	Status       string                 `json:"status"`
	StartTime    int64                  `json:"startTime"` // unix microseconds
	Duration     int64                  `json:"duration"`  // microseconds
	Attributes   map[string]interface{} `json:"attributes,omitempty"`
	Events       []SpanEvent            `json:"events,omitempty"`
}

// SpanEvent represents a timestamped event within a span.
type SpanEvent struct {
	Name       string                 `json:"name"`
	Time       int64                  `json:"time"` // unix microseconds
	Attributes map[string]interface{} `json:"attributes,omitempty"`
}

// SpanOption configures a span at creation time.
type SpanOption func(*SpanData)

// WithKind sets the span kind (e.g. "client", "server", "internal").
func WithKind(kind string) SpanOption {
	return func(s *SpanData) { s.Kind = kind }
}

// WithAttributes sets initial attributes on the span.
func WithAttributes(attrs map[string]interface{}) SpanOption {
	return func(s *SpanData) {
		if s.Attributes == nil {
			s.Attributes = make(map[string]interface{})
		}
		for k, v := range attrs {
			s.Attributes[k] = v
		}
	}
}

// WithParent explicitly sets the parent span ID.
func WithParent(parentSpanID string) SpanOption {
	return func(s *SpanData) { s.ParentSpanID = parentSpanID }
}
