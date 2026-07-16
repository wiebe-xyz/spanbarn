package ingest

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

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
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1000000), testLogger())
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
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1000000), testLogger())

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
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1), testLogger())
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
	tb := NewTraceBuffer(10*time.Millisecond, lookup, testLogger())
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

// TestTraceBuffer_CapDropsNewTraces pins the memory bound: once maxSpans is
// reached, spans for traces the buffer has not seen are dropped rather than
// growing the map. Spans wait out the full TTL before any sampling decision, so
// without this an ingest burst can OOM the reader pod.
func TestTraceBuffer_CapDropsNewTraces(t *testing.T) {
	tb := NewTraceBuffer(time.Hour, NewStaticRatioLookup(1), testLogger())
	tb.maxSpans = 2

	tb.Add(span("t1", "s1", "", "op", "OK"))
	tb.Add(span("t2", "s1", "", "op", "OK"))
	tb.Add(span("t3", "s1", "", "op", "OK")) // over cap — dropped

	tb.mu.Lock()
	held, dropped := len(tb.traces), tb.dropped
	tb.mu.Unlock()

	if held != 2 {
		t.Fatalf("buffered traces = %d, want 2 (third should be dropped)", held)
	}
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
}

// TestTraceBuffer_CapKeepsErrorTraces guards the contract that made removing the
// self-instrument sampling bypass safe: error traces must survive even when the
// buffer is over capacity.
func TestTraceBuffer_CapKeepsErrorTraces(t *testing.T) {
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1000000), testLogger())
	tb.maxSpans = 1

	// First span creates the trace and marks it an error.
	tb.Add(span("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "s1", "", "op", "ERROR"))
	// Now at cap: a further span on the same error trace is still accepted...
	tb.Add(span("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "s2", "s1", "db.query", "OK"))
	// ...but a brand-new non-error trace is not.
	tb.Add(span("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "s1", "", "op", "OK"))

	tb.Flush(time.Now().Add(time.Second))

	select {
	case spans := <-tb.Out:
		if len(spans) != 2 {
			t.Fatalf("want 2 spans on the error trace, got %d", len(spans))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("error trace was dropped at cap — the always-pass contract is broken")
	}
}

// TestTraceBuffer_FlushReleasesCapBudget ensures the cap is not a one-way latch:
// flushed traces must return their span budget, or the buffer wedges permanently
// after the first burst.
func TestTraceBuffer_FlushReleasesCapBudget(t *testing.T) {
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1), testLogger())
	tb.maxSpans = 1

	tb.Add(span("t1", "s1", "", "op", "OK"))
	tb.Flush(time.Now().Add(time.Second))

	tb.mu.Lock()
	count := tb.spanCount
	tb.mu.Unlock()
	if count != 0 {
		t.Fatalf("spanCount after flush = %d, want 0", count)
	}

	// The buffer must accept work again.
	tb.Add(span("t2", "s1", "", "op", "OK"))
	tb.mu.Lock()
	held, dropped := len(tb.traces), tb.dropped
	tb.mu.Unlock()
	if held != 1 || dropped != 0 {
		t.Fatalf("after flush: traces=%d dropped=%d, want 1 and 0", held, dropped)
	}
}
