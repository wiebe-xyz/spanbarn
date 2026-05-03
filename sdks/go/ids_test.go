package spanbarn

import (
	"testing"
)

func TestGenerateTraceID(t *testing.T) {
	id := generateTraceID()
	if len(id) != 32 {
		t.Errorf("expected 32 hex chars, got %d: %q", len(id), id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex character in trace ID: %c", c)
		}
	}
}

func TestGenerateSpanID(t *testing.T) {
	id := generateSpanID()
	if len(id) != 16 {
		t.Errorf("expected 16 hex chars, got %d: %q", len(id), id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex character in span ID: %c", c)
		}
	}
}

func TestIDUniqueness(t *testing.T) {
	const count = 1000
	traceIDs := make(map[string]bool, count)
	spanIDs := make(map[string]bool, count)

	for i := 0; i < count; i++ {
		tid := generateTraceID()
		if traceIDs[tid] {
			t.Fatalf("duplicate trace ID at iteration %d: %s", i, tid)
		}
		traceIDs[tid] = true

		sid := generateSpanID()
		if spanIDs[sid] {
			t.Fatalf("duplicate span ID at iteration %d: %s", i, sid)
		}
		spanIDs[sid] = true
	}
}
