package ingest

import (
	"fmt"
	"testing"
	"time"
)

// unsampledID returns a trace ID that ratio 1000000 skips: its first 8 bytes
// are i+1, which is never a multiple of the ratio for small i.
func unsampledID(i int) string {
	return fmt.Sprintf("%016x%016x", i+1, i)
}

// capRescueBuffer is a 10-span buffer whose TTL never expires during a test,
// so every decision before Close is made by eviction. The eviction clock is
// pinned to at, and Flush and Close are called with at as well, so the hourly
// floor sees one hour throughout.
func capRescueBuffer(t *testing.T, rules RescueRules) (*TraceBuffer, time.Time) {
	t.Helper()
	at := time.Now().Truncate(time.Hour).Add(time.Minute)
	tb := NewTraceBufferWithRules(time.Hour, 10, rules, NewStaticRatioLookup(1000000), testLogger())
	tb.now = func() time.Time { return at }
	return tb, at
}

// TestCapEvictionKeepsFloorTraces fills the buffer past its cap with clean
// traces the ratio sample skips, the way prod runs all day (#339). The first
// trace of each root operation in the hour is the one the floor keeps, so
// eviction must store it with weight 1 and shed the rest.
func TestCapEvictionKeepsFloorTraces(t *testing.T) {
	tb, at := capRescueBuffer(t, RescueRules{MinTracesPerHour: 1})

	tb.Add(rootSpan(unsampledID(0), "GET /security", 1000))
	for i := 1; i <= 20; i++ {
		tb.Add(rootSpan(unsampledID(i), "GET /health/ready", 100))
	}

	// 21 spans into a 10-span cap: 11 evictions. The first two victims are
	// the first trace of each operation; the next nine are shed.
	st := tb.Stats()
	if st.RescuedAtCapSpans != 2 || st.EvictedSacrificialSpans != 9 || st.BufferedSpans != 10 {
		t.Fatalf("stats = rescued %d, shed %d, buffered %d; want 2, 9, 10",
			st.RescuedAtCapSpans, st.EvictedSacrificialSpans, st.BufferedSpans)
	}

	tb.Flush(at)
	got := drain(tb)
	if len(got) != 2 {
		t.Fatalf("flush delivered %d traces, want the 2 kept at eviction", len(got))
	}
	for _, id := range []string{unsampledID(0), unsampledID(1)} {
		spans, ok := got[id]
		if !ok {
			t.Fatalf("trace %s (first of its operation) was not delivered", id)
		}
		if spans[0].SampleWeight != 1 {
			t.Errorf("trace %s: SampleWeight = %d, want 1", id, spans[0].SampleWeight)
		}
	}

	// The floor is spent for both operations in this hour: another rare trace
	// (which evicts one more health trace) and the health traces still
	// buffered are all dropped. Close stores one carrier per operation for the
	// drops, so each operation's weights add up to the traces it had.
	tb.Add(rootSpan(unsampledID(100), "GET /security", 1000))
	out := collect(tb)
	if res := tb.Close(at); res.Kept != 2 {
		t.Fatalf("Close kept %d spans, want the 2 carriers", res.Kept)
	}
	totals := map[string]int{}
	for _, s := range <-out {
		totals[s.Name] += s.SampleWeight
	}
	for _, s := range got {
		totals[s[0].Name] += s[0].SampleWeight
	}
	if totals["GET /security"] != 2 || totals["GET /health/ready"] != 20 {
		t.Errorf("weight totals = %v, want GET /security 2 and GET /health/ready 20", totals)
	}
}

// TestCapEvictionKeepsSlowTraces checks the slow-root rule at eviction: a
// slow trace the ratio sample skips is kept when eviction picks it.
func TestCapEvictionKeepsSlowTraces(t *testing.T) {
	tb, at := capRescueBuffer(t, RescueRules{SlowThresholdUs: 500})
	defer tb.Close(at)

	tb.Add(rootSpan(unsampledID(0), "GET /export", 5000))
	for i := 1; i <= 10; i++ {
		tb.Add(rootSpan(unsampledID(i), "GET /export", 100))
	}

	tb.Flush(at)
	got := drain(tb)
	spans, ok := got[unsampledID(0)]
	if !ok || len(got) != 1 {
		t.Fatalf("flush delivered %d traces (slow one present: %v), want only the slow trace", len(got), ok)
	}
	if spans[0].SampleWeight != 1 {
		t.Errorf("slow trace SampleWeight = %d, want 1", spans[0].SampleWeight)
	}
}
