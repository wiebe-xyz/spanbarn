package ingest

import (
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// These IDs never pass ratio 1000000 (see TestTraceBuffer_NonErrorDroppedByRatio).
const (
	noSampleID  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	noSampleID2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	noSampleID3 = "cccccccccccccccccccccccccccccccc"
	noSampleID4 = "dddddddddddddddddddddddddddddddd"
)

func rescueBuffer(rules RescueRules) *TraceBuffer {
	return NewTraceBufferWithRules(10*time.Millisecond, 0, rules, NewStaticRatioLookup(1000000), testLogger())
}

func drained(tb *TraceBuffer) bool {
	select {
	case <-tb.Out:
		return true
	case <-time.After(30 * time.Millisecond):
		return false
	}
}

func rootSpan(traceID, name string, durUs int64) model.SpanRecord {
	r := span(traceID, "s1", "", name, "OK")
	r.DurationUs = durUs
	return r
}

func TestRescue_RareRootOperationKept(t *testing.T) {
	tb := rescueBuffer(RescueRules{MinTracesPerHour: 1})
	tb.Add(rootSpan(noSampleID, "maintenance.purge", 1000))
	tb.Flush(time.Now().Add(time.Second))
	if !drained(tb) {
		t.Fatal("first trace of a rare operation in the hour must be kept")
	}
}

func TestRescue_FloorLimitsPerOperationAndHour(t *testing.T) {
	tb := rescueBuffer(RescueRules{MinTracesPerHour: 1})
	now := time.Now().Add(time.Second)
	tb.Add(rootSpan(noSampleID, "op.a", 1000))
	tb.Add(rootSpan(noSampleID2, "op.a", 1000))
	tb.Add(rootSpan(noSampleID3, "op.b", 1000))
	tb.Flush(now)

	kept := 0
	for drained(tb) {
		kept++
	}
	if kept != 2 {
		t.Fatalf("kept %d traces, want 2 (one per operation)", kept)
	}

	// The next hour opens a fresh bucket.
	tb.Add(rootSpan(noSampleID4, "op.a", 1000))
	tb.Flush(now.Add(time.Hour))
	if !drained(tb) {
		t.Fatal("a new hour must admit the operation again")
	}
}

func TestRescue_SlowRootKept(t *testing.T) {
	tb := rescueBuffer(RescueRules{SlowThresholdUs: 500_000})
	tb.Add(rootSpan(noSampleID, "slow.op", 5_000_000))
	tb.Add(rootSpan(noSampleID2, "fast.op", 1_000))
	tb.Flush(time.Now().Add(time.Second))

	if !drained(tb) {
		t.Fatal("slow root span trace must be kept")
	}
	if drained(tb) {
		t.Fatal("fast trace must still be sampled out when the floor is off")
	}
}

func TestRescue_SlowChildDoesNotKeepTrace(t *testing.T) {
	tb := rescueBuffer(RescueRules{SlowThresholdUs: 500_000})
	tb.Add(rootSpan(noSampleID, "op", 1_000))
	child := span(noSampleID, "s2", "s1", "db", "OK")
	child.DurationUs = 9_000_000
	tb.Add(child)
	tb.Flush(time.Now().Add(time.Second))
	if drained(tb) {
		t.Fatal("only the root span decides the slow rule")
	}
}

func TestRescue_DisabledByZeroRules(t *testing.T) {
	tb := rescueBuffer(RescueRules{})
	tb.Add(rootSpan(noSampleID, "op", 9_000_000))
	tb.Flush(time.Now().Add(time.Second))
	if drained(tb) {
		t.Fatal("zero rules must keep the old behaviour")
	}
}

func TestRescue_RatioKeepStillKeeps(t *testing.T) {
	tb := NewTraceBufferWithRules(10*time.Millisecond, 0, RescueRules{MinTracesPerHour: 1}, NewStaticRatioLookup(1), testLogger())
	tb.Add(rootSpan(noSampleID, "op", 1))
	tb.Add(rootSpan(noSampleID2, "op", 1))
	tb.Flush(time.Now().Add(time.Second))
	n := 0
	for drained(tb) {
		n++
	}
	if n != 2 {
		t.Fatalf("ratio 1 keeps everything, got %d", n)
	}
}
