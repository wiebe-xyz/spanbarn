package ingest

import (
	"fmt"
	"testing"
)

func TestIngestSampler_ErrorAlwaysKept(t *testing.T) {
	s := NewIngestSampler()
	// Exhaust the rate window for an operation.
	for i := 0; i < DefaultMaxPerMinute+10; i++ {
		s.Keep(1, "GET /api/health", "OK", fmt.Sprintf("trace%d", i))
	}
	// Error span must still pass even after the cap is hit.
	if !s.Keep(1, "GET /api/health", "error", "trace-error") {
		t.Fatal("error span was dropped")
	}
}

func TestIngestSampler_RateCapDropsNonError(t *testing.T) {
	s := NewIngestSampler()
	kept := 0
	for i := 0; i < DefaultMaxPerMinute+50; i++ {
		if s.Keep(1, "GET /api/health", "OK", fmt.Sprintf("t%d", i)) {
			kept++
		}
	}
	if kept != DefaultMaxPerMinute {
		t.Fatalf("expected %d kept, got %d", DefaultMaxPerMinute, kept)
	}
}

func TestIngestSampler_ErrorTraceAllowlist(t *testing.T) {
	s := NewIngestSampler()
	// Exhaust rate cap for this operation.
	for i := 0; i < DefaultMaxPerMinute+10; i++ {
		s.Keep(1, "op", "OK", "other-trace")
	}
	// Mark trace "t1" as having an error.
	if !s.Keep(1, "op", "error", "t1") {
		t.Fatal("error span dropped")
	}
	// Subsequent OK spans from the SAME trace must pass (context preservation).
	if !s.Keep(1, "op", "OK", "t1") {
		t.Fatal("span from error trace was dropped")
	}
	// But a different trace stays rate-capped.
	if s.Keep(1, "op", "OK", "other-trace-2") {
		t.Fatal("unrelated span should still be rate-capped")
	}
}

func TestIngestSampler_DifferentProjectsIndependent(t *testing.T) {
	s := NewIngestSampler()
	// Exhaust project 1.
	for i := 0; i < DefaultMaxPerMinute+5; i++ {
		s.Keep(1, "ping", "OK", "t")
	}
	// Project 2 should still pass.
	if !s.Keep(2, "ping", "OK", "t") {
		t.Fatal("project 2 was incorrectly rate-capped by project 1")
	}
}
