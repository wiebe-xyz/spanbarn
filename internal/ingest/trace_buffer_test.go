package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func span(traceID, spanID, parentID, name, status string) model.SpanRecord {
	return model.SpanRecord{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentID,
		Name:         name,
		Status:       status,
		ProjectID:    1,
	}
}

func TestTraceBuffer_ErrorTraceAlwaysKept(t *testing.T) {
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1000000))
	tb.Add(span("trace1", "s1", "", "GET /health", "OK"))
	tb.Add(span("trace1", "s2", "s1", "db.query", "ERROR"))

	tb.Flush(time.Now().Add(time.Second))

	select {
	case spans := <-tb.Out:
		if len(spans) != 2 {
			t.Fatalf("want 2 spans for error trace, got %d", len(spans))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no spans emitted for error trace")
	}
}

func TestTraceBuffer_NonErrorDroppedByRatio(t *testing.T) {
	// Ratio 1000000 so almost nothing passes; deterministically check a known trace.
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1000000))

	// Use a trace ID that we know does NOT sample at ratio 1000000.
	// "aaaaaaaaaaaaaaaa..." → upper bytes all 0xaa → 0xaaaa...aa % 1000000 ≠ 0
	noSampleID := "aaaaaaaaaaaaaaaa" + "aaaaaaaaaaaaaaaa"
	tb.Add(span(noSampleID, "s1", "", "GET /health", "OK"))
	tb.Flush(time.Now().Add(time.Second))

	select {
	case spans := <-tb.Out:
		t.Fatalf("expected trace to be dropped, got %d spans", len(spans))
	case <-time.After(50 * time.Millisecond):
		// correct — nothing emitted
	}
}

func TestTraceBuffer_RatioOneKeepsAll(t *testing.T) {
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1))
	tb.Add(span("trace1", "s1", "", "POST /api/data", "OK"))
	tb.Flush(time.Now().Add(time.Second))

	select {
	case <-tb.Out:
		// correct
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ratio=1 should keep everything")
	}
}

func TestTraceBuffer_RootSpanDeterminesOperation(t *testing.T) {
	// The root span (no parentSpanID) should determine the ratio lookup key.
	called := false
	lookup := &opCheckLookup{check: func(op string) {
		if op != "root-op" {
			t.Errorf("expected root-op, got %q", op)
		}
		called = true
	}}
	tb := NewTraceBuffer(10*time.Millisecond, lookup)
	tb.Add(span("t1", "child", "root", "child-op", "OK")) // child first
	tb.Add(span("t1", "root", "", "root-op", "OK"))       // root second
	tb.Flush(time.Now().Add(time.Second))
	if !called {
		t.Fatal("lookup not called")
	}
}

// opCheckLookup calls fn with the operation name and returns ratio=1 (keep all).
type opCheckLookup struct{ check func(string) }

func (l *opCheckLookup) Ratio(_ context.Context, _ int64, op string) int {
	l.check(op)
	return 1
}
