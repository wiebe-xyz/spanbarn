package retention

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestRunOnceTracesPhasesWithCounts pins that a retention cycle's trace holds
// the cycle and its phase spans with db.calls counts, and no span per
// statement. One prod cycle on 2026-10-29 stored 2996 spans, nearly all of
// them sqlite.* children.
func TestRunOnceTracesPhasesWithCounts(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	w, repo := setupTestWorker(t, Config{InterestingRetentionHours: 1})
	insertTestSpans(t, repo, 20, "ok", 1000, time.Now().Add(-2*time.Hour))
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range sr.Ended() {
		if strings.HasPrefix(s.Name(), "retention.") {
			byName[s.Name()] = s
		}
	}
	cycle, ok := byName["retention.cycle"]
	if !ok {
		t.Fatal("no retention.cycle span")
	}
	var inCycle []string
	for _, s := range sr.Ended() {
		if s.SpanContext().TraceID() == cycle.SpanContext().TraceID() && strings.HasPrefix(s.Name(), "sqlite.") {
			inCycle = append(inCycle, s.Name())
		}
	}
	if len(inCycle) > 0 {
		t.Fatalf("cycle trace holds %d sqlite span(s): %v", len(inCycle), inCycle)
	}

	var total int64
	for _, name := range []string{"retention.drain_spans", "retention.purge_span_derived", "retention.purge_housekeeping"} {
		s, ok := byName[name]
		if !ok {
			t.Fatalf("no %s span", name)
		}
		if s.Parent().SpanID() != cycle.SpanContext().SpanID() {
			t.Errorf("%s is not a child of retention.cycle", name)
		}
		for _, kv := range s.Attributes() {
			if kv.Key == "db.calls" {
				total += kv.Value.AsInt64()
			}
		}
	}
	if total == 0 {
		t.Fatal("phase spans carry no db.calls: the cycle's statements were neither traced nor counted")
	}
}
