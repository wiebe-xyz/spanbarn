package spanbarn

import (
	"context"
	"testing"
	"time"
)

func TestInit(t *testing.T) {
	c := Init(Config{
		Endpoint: "http://localhost:9999",
		APIKey:   "test-key",
		Service:  "test-svc",
	})
	defer Shutdown()

	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.cfg.FlushInterval != 5*time.Second {
		t.Errorf("expected default flush interval 5s, got %v", c.cfg.FlushInterval)
	}
	if c.cfg.MaxBatchSize != 100 {
		t.Errorf("expected default max batch size 100, got %d", c.cfg.MaxBatchSize)
	}
	if c.cfg.MaxQueueSize != 1000 {
		t.Errorf("expected default max queue size 1000, got %d", c.cfg.MaxQueueSize)
	}
}

func TestStartSpan(t *testing.T) {
	c := NewClient(Config{
		Endpoint: "http://localhost:9999",
		APIKey:   "test-key",
		Service:  "test-svc",
		Disabled: true,
	})

	ctx, span := c.Start(context.Background(), "test-op")
	if span == nil {
		t.Fatal("expected non-nil span")
	}
	if span.data.Name != "test-op" {
		t.Errorf("expected name 'test-op', got %q", span.data.Name)
	}
	if span.data.Service != "test-svc" {
		t.Errorf("expected service 'test-svc', got %q", span.data.Service)
	}
	if len(span.data.TraceID) != 32 {
		t.Errorf("expected 32-char trace ID, got %d", len(span.data.TraceID))
	}
	if len(span.data.SpanID) != 16 {
		t.Errorf("expected 16-char span ID, got %d", len(span.data.SpanID))
	}
	if span.data.Kind != "internal" {
		t.Errorf("expected kind 'internal', got %q", span.data.Kind)
	}

	// Context should have span context
	sc, ok := spanContextFromContext(ctx)
	if !ok {
		t.Fatal("expected span context in returned context")
	}
	if sc.TraceID != span.data.TraceID {
		t.Error("context trace ID mismatch")
	}
}

func TestStartSpanWithParentContext(t *testing.T) {
	c := NewClient(Config{
		Endpoint: "http://localhost:9999",
		APIKey:   "test-key",
		Disabled: true,
	})

	parentCtx := withSpanContext(context.Background(), spanContext{
		TraceID: "aaaabbbbccccddddaaaabbbbccccdddd",
		SpanID:  "1111222233334444",
	})

	_, span := c.Start(parentCtx, "child-op")
	if span.data.TraceID != "aaaabbbbccccddddaaaabbbbccccdddd" {
		t.Errorf("expected parent trace ID, got %q", span.data.TraceID)
	}
	if span.data.ParentSpanID != "1111222233334444" {
		t.Errorf("expected parent span ID, got %q", span.data.ParentSpanID)
	}
}

func TestEnqueue(t *testing.T) {
	c := NewClient(Config{
		Endpoint:      "http://localhost:9999",
		APIKey:        "test-key",
		Service:       "test-svc",
		FlushInterval: 1 * time.Hour, // don't auto-flush
	})
	defer c.Shutdown()

	_, span := c.Start(context.Background(), "enqueue-test")
	span.End()

	// Give a moment for the channel send
	time.Sleep(10 * time.Millisecond)

	if len(c.queue) != 1 {
		t.Errorf("expected 1 span in queue, got %d", len(c.queue))
	}
}

func TestShutdown(t *testing.T) {
	c := NewClient(Config{
		Endpoint:      "http://localhost:9999",
		APIKey:        "test-key",
		FlushInterval: 1 * time.Hour,
	})

	_, span := c.Start(context.Background(), "shutdown-test")
	span.End()

	err := c.Shutdown()
	if err != nil {
		t.Errorf("unexpected shutdown error: %v", err)
	}

	// After shutdown, queue should be drained
	if len(c.queue) != 0 {
		t.Errorf("expected empty queue after shutdown, got %d", len(c.queue))
	}
}

func TestDisabled(t *testing.T) {
	c := NewClient(Config{
		Endpoint: "http://localhost:9999",
		APIKey:   "test-key",
		Disabled: true,
	})

	_, span := c.Start(context.Background(), "disabled-test")
	span.SetAttribute("key", "val")
	span.End()

	// Should not enqueue
	if len(c.queue) != 0 {
		t.Errorf("expected empty queue for disabled client, got %d", len(c.queue))
	}

	// Shutdown should be no-op
	err := c.Shutdown()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMaxQueueSize(t *testing.T) {
	c := NewClient(Config{
		Endpoint:      "http://localhost:9999",
		APIKey:        "test-key",
		MaxQueueSize:  5,
		FlushInterval: 1 * time.Hour, // prevent auto-flush
	})
	defer c.Shutdown()

	// Enqueue more than capacity
	for i := 0; i < 10; i++ {
		_, span := c.Start(context.Background(), "overflow-test")
		span.End()
	}

	time.Sleep(10 * time.Millisecond)

	if len(c.queue) > 5 {
		t.Errorf("expected at most 5 spans in queue, got %d", len(c.queue))
	}
}

func TestBeforeSend(t *testing.T) {
	c := NewClient(Config{
		Endpoint:      "http://localhost:9999",
		APIKey:        "test-key",
		FlushInterval: 1 * time.Hour,
		BeforeSend: func(s *SpanData) *SpanData {
			s.Name = "modified-" + s.Name
			return s
		},
	})
	defer c.Shutdown()

	_, span := c.Start(context.Background(), "original")
	span.End()

	time.Sleep(10 * time.Millisecond)

	select {
	case s := <-c.queue:
		if s.Name != "modified-original" {
			t.Errorf("expected modified name, got %q", s.Name)
		}
	default:
		t.Error("expected span in queue")
	}
}

func TestBeforeSendNil(t *testing.T) {
	c := NewClient(Config{
		Endpoint:      "http://localhost:9999",
		APIKey:        "test-key",
		FlushInterval: 1 * time.Hour,
		BeforeSend: func(s *SpanData) *SpanData {
			return nil // drop all spans
		},
	})
	defer c.Shutdown()

	_, span := c.Start(context.Background(), "dropped")
	span.End()

	time.Sleep(10 * time.Millisecond)

	if len(c.queue) != 0 {
		t.Errorf("expected empty queue when BeforeSend returns nil, got %d", len(c.queue))
	}
}
