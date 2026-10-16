package repository

import (
	"context"
	"testing"
	"time"
)

// A trace whose error is in a child span groups under its ok root, and a trace
// whose spans now live only in error_samples is still found. Clean traces and
// other projects are left out.
func TestQueryErrorTraceGroupsMatchesChildErrors(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	older := now.Add(-2 * time.Hour)
	newer := now.Add(-1 * time.Hour)
	at := func(s Span, ts time.Time) Span { s.IngestedAt = ts; return s }
	// A trace retention already moved: its summary stays, its error span is
	// only in error_samples.
	moved := at(tsSpan("M", "m1", "", "nightly.sweep", "cron", "error", 500, 10), older)
	if err := repo.InsertSpans([]Span{moved}); err != nil {
		t.Fatalf("insert moved: %v", err)
	}
	if err := repo.InsertErrorSamples([]Span{moved}); err != nil {
		t.Fatalf("insert error sample: %v", err)
	}
	if _, err := repo.DeleteSpansOlderThan(now.Add(time.Hour)); err != nil {
		t.Fatalf("delete spans: %v", err)
	}
	if err := repo.InsertSpans([]Span{
		// Two page loads with a failed client call under an ok root.
		at(tsSpan("P1", "p1", "", "page /app", "web", "ok", 1000, 3_000_000), older),
		at(tsSpan("P1", "p1c", "p1", "GET /pending", "web", "error", 1100, 50), older),
		at(tsSpan("P2", "p2", "", "page /app", "web", "ok", 2000, 4_000_000), newer),
		at(tsSpan("P2", "p2c", "p2", "GET /pending", "web", "error", 2100, 50), newer),
		at(tsSpan("P2", "p2d", "p2", "GET /me", "web", "error", 2200, 50), newer),
		// A clean page load.
		at(tsSpan("C", "c1", "", "page /app", "web", "ok", 3000, 1_000_000), newer),
	}); err != nil {
		t.Fatalf("insert spans: %v", err)
	}
	// Another project's error trace.
	other := at(tsSpan("O", "o1", "", "page /app", "web", "error", 1000, 10), newer)
	other.ProjectID = 2
	if err := repo.InsertSpans([]Span{other}); err != nil {
		t.Fatalf("insert other project: %v", err)
	}

	groups, err := repo.QueryErrorTraceGroups(ctx, SpanFilter{ProjectID: 1, From: now.Add(-3 * time.Hour), To: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("QueryErrorTraceGroups: %v", err)
	}
	byOp := map[string]ErrorTraceGroup{}
	for _, g := range groups {
		byOp[g.Operation] = g
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %+v, want page /app and nightly.sweep", groups)
	}
	page := byOp["page /app"]
	if len(page.Durations) != 2 || page.ExampleTraceID != "P2" {
		t.Fatalf("page /app group = %+v, want 2 traces with example P2", page)
	}
	ops := map[string]ErrorOperation{}
	for _, op := range page.ErrorOperations {
		ops[op.Operation] = op
	}
	if ops["GET /pending"].Traces != 2 || ops["GET /pending"].ExampleTraceID != "P2" {
		t.Errorf("GET /pending = %+v, want 2 traces, example P2", ops["GET /pending"])
	}
	if ops["GET /me"].Traces != 1 {
		t.Errorf("GET /me = %+v, want 1 trace", ops["GET /me"])
	}
	sweep := byOp["nightly.sweep"]
	if len(sweep.Durations) != 1 || len(sweep.ErrorOperations) != 1 || sweep.ErrorOperations[0].Operation != "nightly.sweep" {
		t.Errorf("nightly.sweep group = %+v, want its error read from error_samples", sweep)
	}
}
