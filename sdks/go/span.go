package spanbarn

import (
	"sync"
	"time"
)

// Span represents an in-flight operation being traced.
type Span struct {
	client *Client
	data   SpanData
	ended  bool
	mu     sync.Mutex
}

// SetAttribute sets a single attribute on the span.
func (s *Span) SetAttribute(key string, value interface{}) *Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return s
	}
	if s.data.Attributes == nil {
		s.data.Attributes = make(map[string]interface{})
	}
	s.data.Attributes[key] = value
	return s
}

// SetAttributes sets multiple attributes on the span.
func (s *Span) SetAttributes(attrs map[string]interface{}) *Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return s
	}
	if s.data.Attributes == nil {
		s.data.Attributes = make(map[string]interface{})
	}
	for k, v := range attrs {
		s.data.Attributes[k] = v
	}
	return s
}

// SetStatus sets the span status string.
func (s *Span) SetStatus(status string) *Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return s
	}
	s.data.Status = status
	return s
}

// AddEvent adds a timestamped event to the span.
func (s *Span) AddEvent(name string, attrs ...map[string]interface{}) *Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return s
	}
	ev := SpanEvent{
		Name: name,
		Time: time.Now().UnixMicro(),
	}
	if len(attrs) > 0 && attrs[0] != nil {
		ev.Attributes = attrs[0]
	}
	s.data.Events = append(s.data.Events, ev)
	return s
}

// Ok sets the span status to "ok".
func (s *Span) Ok() *Span {
	return s.SetStatus("ok")
}

// Error sets the span status to "error" and records the error message.
func (s *Span) Error(err error) *Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return s
	}
	s.data.Status = "error"
	if s.data.Attributes == nil {
		s.data.Attributes = make(map[string]interface{})
	}
	if err != nil {
		s.data.Attributes["error.message"] = err.Error()
	}
	return s
}

// End completes the span, calculates its duration, and enqueues it for export.
// Calling End a second time is a no-op.
func (s *Span) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	s.data.Duration = time.Now().UnixMicro() - s.data.StartTime
	if s.client != nil {
		data := s.data // copy
		s.client.enqueue(&data)
	}
}
