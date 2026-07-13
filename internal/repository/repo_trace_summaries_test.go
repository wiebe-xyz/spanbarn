package repository

import (
	"context"
	"testing"
	"time"
)

func tsSpan(trace, span, parent, name, service, status string, startUs, durUs int64) Span {
	return Span{
		ProjectID: 1, TraceID: trace, SpanID: span, ParentSpanID: parent,
		Name: name, Service: service, Kind: "server", Status: status,
		StartTimeUs: startUs, DurationUs: durUs, Attributes: "{}", Events: "[]",
	}
}

// InsertSpans upserts trace_summaries, so SearchTraceSummaries reads back the
// rolled-up rows without touching the spans table's group-by.
func TestTraceSummariesUpsertAndSearch(t *testing.T) {
	repo := setupTestDB(t)

	// Trace A: root + error child.
	if err := repo.InsertSpans([]Span{
		tsSpan("A", "a1", "", "GET /orders", "web", "ok", 1000, 5000),
		tsSpan("A", "a2", "a1", "SELECT", "db", "error", 1200, 300),
	}); err != nil {
		t.Fatalf("insert A: %v", err)
	}
	// Trace B: single root, no error.
	if err := repo.InsertSpans([]Span{
		tsSpan("B", "b1", "", "GET /health", "web", "ok", 500, 80),
	}); err != nil {
		t.Fatalf("insert B: %v", err)
	}

	rows, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1}, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 summaries, got %d: %+v", len(rows), rows)
	}
	byID := map[string]TraceSummaryRow{}
	for _, r := range rows {
		byID[r.TraceID] = r
	}
	a := byID["A"]
	if a.SpanCount != 2 {
		t.Errorf("A span count = %d, want 2", a.SpanCount)
	}
	if !a.HasError {
		t.Errorf("A should have error")
	}
	if a.RootName != "GET /orders" || a.RootService != "web" || a.RootDuration != 5000 {
		t.Errorf("A root = %q/%q/%d, want GET /orders/web/5000", a.RootName, a.RootService, a.RootDuration)
	}
	if a.StartTimeUs != 1000 {
		t.Errorf("A start = %d, want 1000 (min)", a.StartTimeUs)
	}
	if b := byID["B"]; b.SpanCount != 1 || b.HasError {
		t.Errorf("B = %+v, want 1 span, no error", b)
	}
}

// Late-arriving spans of an already-summarised trace accumulate: count sums,
// has_error ORs, start_time takes the min, and a child batch does not clobber
// the recorded root.
func TestTraceSummariesAccumulate(t *testing.T) {
	repo := setupTestDB(t)

	if err := repo.InsertSpans([]Span{tsSpan("A", "a1", "", "root", "web", "ok", 1000, 500)}); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	// A later batch with only a child (no root) that errored and started earlier.
	if err := repo.InsertSpans([]Span{tsSpan("A", "a2", "a1", "child", "db", "error", 900, 100)}); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	rows, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1}, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 summary, got %d", len(rows))
	}
	r := rows[0]
	if r.SpanCount != 2 {
		t.Errorf("span count = %d, want 2 (accumulated)", r.SpanCount)
	}
	if !r.HasError {
		t.Errorf("has_error should OR to true after the late error span")
	}
	if r.RootName != "root" {
		t.Errorf("root name = %q, want 'root' (child batch must not clobber it)", r.RootName)
	}
	if r.StartTimeUs != 900 {
		t.Errorf("start = %d, want 900 (min after earlier late span)", r.StartTimeUs)
	}
}

// The production path is the staging flush: CommitStagingFlush must roll the
// persisted spans into a summary in the same transaction.
func TestTraceSummariesViaStagingFlush(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	spans := []Span{
		tsSpan("A", "a1", "", "GET /x", "web", "ok", 1000, 400),
		tsSpan("A", "a2", "a1", "db.query", "db", "error", 1100, 50),
	}
	for i := range spans {
		spans[i].IngestedAt = now
	}
	if err := repo.CommitStagingFlush(ctx, []string{"A"}, spans); err != nil {
		t.Fatalf("commit staging flush: %v", err)
	}

	rows, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1}, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 || rows[0].SpanCount != 2 || !rows[0].HasError || rows[0].RootName != "GET /x" {
		t.Fatalf("staging flush did not roll up the trace: %+v", rows)
	}
}

// Retention drops summaries in lockstep with their spans: boring-sampled by
// expires_at, non-error at the interesting cutoff, error at the (later) error
// cutoff.
func TestTraceSummariesRetention(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(trace, status string, ingested time.Time, expires *time.Time) {
		s := tsSpan(trace, trace+"root", "", "root", "web", status, 1000, 100)
		s.IngestedAt = ingested
		s.ExpiresAt = expires
		if err := repo.InsertSpans([]Span{s}); err != nil {
			t.Fatalf("insert %s: %v", trace, err)
		}
	}
	pastExpiry := now.Add(-1 * time.Hour)
	mk("A", "ok", now.Add(-100*time.Hour), nil)       // interesting, non-error, old
	mk("B", "error", now.Add(-100*time.Hour), nil)    // interesting, error, old
	mk("C", "ok", now.Add(-2*time.Hour), &pastExpiry) // boring-sampled, expired

	// Boring cleanup removes C only.
	if _, err := repo.DeleteExpiredTraceSummaries(ctx, now); err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	// interestingCutoff = -50h removes A (non-error, ingested -100h); errorCutoff
	// = -200h keeps B (error, ingested -100h is newer than the error cutoff).
	if _, err := repo.DeleteTraceSummariesOlderThan(ctx, now.Add(-50*time.Hour), now.Add(-200*time.Hour)); err != nil {
		t.Fatalf("delete older: %v", err)
	}

	rows, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1}, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 || rows[0].TraceID != "B" {
		t.Fatalf("want only B to survive retention, got %+v", rows)
	}
}
