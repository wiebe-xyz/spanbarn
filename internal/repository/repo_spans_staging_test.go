package repository

import (
	"context"
	"testing"
	"time"
)

func stagingSpan(trace, span, status string) Span {
	return Span{
		ProjectID: 1, TraceID: trace, SpanID: span, Name: "op", Service: "svc",
		Kind: "server", Status: status, StartTimeUs: 1, DurationUs: 1,
		Attributes: "{}", Events: "[]",
	}
}

// TestStagingFlushCycle exercises the full Shape-2 path: append spans to staging,
// discover ready traces, gather whole traces, move the interesting one into the
// indexed spans table while dropping the boring one, and confirm staging drains.
func TestStagingFlushCycle(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	// Trace A is interesting (has an error span); trace B is entirely boring.
	spans := []Span{
		stagingSpan("traceA", "a1", "unset"),
		stagingSpan("traceA", "a2", "error"),
		stagingSpan("traceB", "b1", "unset"),
		stagingSpan("traceB", "b2", "unset"),
	}
	if err := repo.InsertSpansStaging(ctx, spans); err != nil {
		t.Fatalf("InsertSpansStaging: %v", err)
	}
	if n, _ := repo.CountStagingRows(ctx); n != 4 {
		t.Fatalf("expected 4 staged rows, got %d", n)
	}

	// Everything staged so far is "ready" relative to a future cutoff.
	ready, err := repo.ReadyStagingTraceIDs(ctx, time.Now().Add(time.Hour), 100)
	if err != nil {
		t.Fatalf("ReadyStagingTraceIDs: %v", err)
	}
	if len(ready) != 2 {
		t.Fatalf("expected 2 ready traces, got %d (%v)", len(ready), ready)
	}

	gathered, err := repo.GetStagingSpansByTraceIDs(ctx, ready)
	if err != nil {
		t.Fatalf("GetStagingSpansByTraceIDs: %v", err)
	}
	if len(gathered) != 4 {
		t.Fatalf("expected 4 gathered spans, got %d", len(gathered)) //nolint
	}

	// Classify: keep every span of a trace that contains an error span.
	interestingTraces := map[string]bool{}
	for _, s := range gathered {
		if s.Status == "error" {
			interestingTraces[s.TraceID] = true
		}
	}
	var interesting []Span
	for _, s := range gathered {
		if interestingTraces[s.TraceID] {
			interesting = append(interesting, s)
		}
	}
	if len(interesting) != 2 {
		t.Fatalf("expected 2 interesting spans (trace A), got %d", len(interesting))
	}

	if err := repo.CommitStagingFlush(ctx, ready, interesting); err != nil {
		t.Fatalf("CommitStagingFlush: %v", err)
	}

	// Staging fully drained.
	if n, _ := repo.CountStagingRows(ctx); n != 0 {
		t.Fatalf("expected staging drained, got %d rows", n)
	}
	// Interesting trace A landed in the indexed spans table...
	got, err := repo.GetTraceByID("traceA")
	if err != nil {
		t.Fatalf("GetTraceByID(traceA): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 spans for trace A in spans, got %d", len(got))
	}
	// ...boring trace B was dropped.
	if b, _ := repo.GetTraceByID("traceB"); len(b) != 0 {
		t.Fatalf("expected trace B dropped, got %d spans", len(b))
	}
}

func TestStagingGCBackstop(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	if err := repo.InsertSpansStaging(ctx, []Span{stagingSpan("t1", "s1", "unset")}); err != nil {
		t.Fatalf("InsertSpansStaging: %v", err)
	}
	// Nothing older than an hour ago yet.
	if d, _ := repo.DeleteStagingOlderThan(ctx, time.Now().Add(-time.Hour)); d != 0 {
		t.Fatalf("expected 0 GC deletes, got %d", d)
	}
	// A future cutoff sweeps everything (bounded-growth guarantee).
	d, err := repo.DeleteStagingOlderThan(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteStagingOlderThan: %v", err)
	}
	if d != 1 {
		t.Fatalf("expected 1 GC delete, got %d", d)
	}
	if n, _ := repo.CountStagingRows(ctx); n != 0 {
		t.Fatalf("expected staging empty after GC, got %d", n)
	}
}
