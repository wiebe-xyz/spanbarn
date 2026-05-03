package spanbarn

import (
	"errors"
	"testing"
	"time"
)

func TestSpanSetAttribute(t *testing.T) {
	s := &Span{data: SpanData{StartTime: time.Now().UnixMicro()}}
	s.SetAttribute("key", "value")
	if s.data.Attributes["key"] != "value" {
		t.Errorf("expected attribute 'key' = 'value', got %v", s.data.Attributes["key"])
	}
}

func TestSpanSetAttributes(t *testing.T) {
	s := &Span{data: SpanData{StartTime: time.Now().UnixMicro()}}
	s.SetAttributes(map[string]interface{}{
		"a": 1,
		"b": "two",
	})
	if s.data.Attributes["a"] != 1 {
		t.Errorf("expected 'a' = 1, got %v", s.data.Attributes["a"])
	}
	if s.data.Attributes["b"] != "two" {
		t.Errorf("expected 'b' = 'two', got %v", s.data.Attributes["b"])
	}
}

func TestSpanSetStatus(t *testing.T) {
	s := &Span{data: SpanData{StartTime: time.Now().UnixMicro()}}
	s.SetStatus("error")
	if s.data.Status != "error" {
		t.Errorf("expected status 'error', got %q", s.data.Status)
	}
	s.Ok()
	if s.data.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", s.data.Status)
	}
}

func TestSpanAddEvent(t *testing.T) {
	s := &Span{data: SpanData{StartTime: time.Now().UnixMicro()}}
	s.AddEvent("test-event", map[string]interface{}{"detail": "yes"})
	if len(s.data.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(s.data.Events))
	}
	ev := s.data.Events[0]
	if ev.Name != "test-event" {
		t.Errorf("expected event name 'test-event', got %q", ev.Name)
	}
	if ev.Attributes["detail"] != "yes" {
		t.Errorf("expected event attribute 'detail' = 'yes', got %v", ev.Attributes["detail"])
	}
	if ev.Time == 0 {
		t.Error("expected non-zero event time")
	}
}

func TestSpanError(t *testing.T) {
	s := &Span{data: SpanData{StartTime: time.Now().UnixMicro()}}
	s.Error(errors.New("something failed"))
	if s.data.Status != "error" {
		t.Errorf("expected status 'error', got %q", s.data.Status)
	}
	if s.data.Attributes["error.message"] != "something failed" {
		t.Errorf("expected error.message attribute, got %v", s.data.Attributes["error.message"])
	}
}

func TestSpanEnd(t *testing.T) {
	s := &Span{data: SpanData{StartTime: time.Now().UnixMicro()}}
	time.Sleep(1 * time.Millisecond) // ensure non-zero duration
	s.End()
	if !s.ended {
		t.Error("expected span to be marked ended")
	}
	if s.data.Duration <= 0 {
		t.Errorf("expected positive duration, got %d", s.data.Duration)
	}
}

func TestSpanEndTwice(t *testing.T) {
	q := make(chan *SpanData, 10)
	c := &Client{
		cfg:   Config{MaxQueueSize: 10},
		queue: q,
	}
	s := &Span{
		client: c,
		data:   SpanData{StartTime: time.Now().UnixMicro()},
	}
	s.End()
	s.End() // should be no-op

	// Only one span should be enqueued
	if len(q) != 1 {
		t.Errorf("expected 1 enqueued span, got %d", len(q))
	}
}

func TestSpanChaining(t *testing.T) {
	s := &Span{data: SpanData{StartTime: time.Now().UnixMicro()}}
	result := s.SetAttribute("a", 1).SetStatus("ok").AddEvent("ev")
	if result != s {
		t.Error("expected chaining to return same span")
	}
}

func TestSpanSetAttributeAfterEnd(t *testing.T) {
	s := &Span{data: SpanData{StartTime: time.Now().UnixMicro()}}
	s.End()
	s.SetAttribute("key", "value")
	if s.data.Attributes != nil && s.data.Attributes["key"] == "value" {
		t.Error("expected SetAttribute to be no-op after End")
	}
}
