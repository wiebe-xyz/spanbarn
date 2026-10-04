package repository

import (
	"context"
	"testing"
	"time"
)

// summaryDurable reads trace_summaries.durable for one trace.
func summaryDurable(t *testing.T, repo *Repository, traceID string) bool {
	t.Helper()
	var d bool
	if err := repo.db.QueryRow(`SELECT durable FROM trace_summaries WHERE trace_id = ?`, traceID).Scan(&d); err != nil {
		t.Fatalf("read summary durable for %s: %v", traceID, err)
	}
	return d
}

// The durable flag is written by both span insert paths, read back by the
// retention scan, and rolled up onto the trace summary.
func TestDurableRoundTrip(t *testing.T) {
	repo := setupTestDB(t)

	durable := tsSpan("D", "d1", "", "commitment-sweep", "cron", "ok", 1000, 50)
	durable.Durable = true
	if err := repo.InsertSpans([]Span{durable, tsSpan("P", "p1", "", "GET /health", "web", "ok", 1000, 50)}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	staged := tsSpan("S", "s1", "", "nightly", "cron", "ok", 1000, 50)
	staged.IngestedAt = time.Now().UTC()
	if err := repo.InsertSpansStaging(context.Background(), []Span{staged}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	staged.Durable = true
	if err := repo.CommitStagingFlush(context.Background(), []string{"S"}, []Span{staged}); err != nil {
		t.Fatalf("flush: %v", err)
	}

	spans, err := repo.GetSpansForAggregation(time.Now().Add(time.Hour), 100)
	if err != nil {
		t.Fatalf("aggregation scan: %v", err)
	}
	got := map[string]bool{}
	for _, s := range spans {
		got[s.TraceID] = s.Durable
	}
	want := map[string]bool{"D": true, "P": false, "S": true}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("span %s durable = %v, want %v", id, got[id], w)
		}
		if d := summaryDurable(t, repo, id); d != w {
			t.Errorf("summary %s durable = %v, want %v", id, d, w)
		}
	}
}

// A later non-durable batch of a durable trace keeps the summary durable.
func TestDurableSummarySticks(t *testing.T) {
	repo := setupTestDB(t)
	root := tsSpan("D", "d1", "", "commitment-sweep", "cron", "ok", 1000, 50)
	root.Durable = true
	if err := repo.InsertSpans([]Span{root}); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	if err := repo.InsertSpans([]Span{tsSpan("D", "d2", "d1", "SELECT", "db", "ok", 1010, 5)}); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	if !summaryDurable(t, repo, "D") {
		t.Fatal("summary lost durable after a non-durable batch")
	}
}

// A durable summary outlives the interesting cutoff and goes at the error
// cutoff, like an error summary; a plain clean summary goes at the interesting
// cutoff.
func TestDurableSummaryKeptUntilErrorCutoff(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	ingested := now.Add(-100 * time.Hour)
	durable := tsSpan("D", "d1", "", "commitment-sweep", "cron", "ok", 1000, 50)
	durable.Durable = true
	durable.IngestedAt = ingested
	plain := tsSpan("P", "p1", "", "GET /health", "web", "ok", 1000, 50)
	plain.IngestedAt = ingested
	if err := repo.InsertSpans([]Span{durable, plain}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	summaries := func() map[string]bool {
		t.Helper()
		rows, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1}, 0)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		got := map[string]bool{}
		for _, r := range rows {
			got[r.TraceID] = true
		}
		return got
	}

	// interestingCutoff = -50h is past both rows; errorCutoff = -200h is not.
	if _, err := repo.DeleteTraceSummariesOlderThan(ctx, now.Add(-50*time.Hour), now.Add(-200*time.Hour)); err != nil {
		t.Fatalf("delete at interesting cutoff: %v", err)
	}
	got := summaries()
	if !got["D"] || got["P"] || len(got) != 1 {
		t.Fatalf("after interesting cutoff want only D, got %v", got)
	}

	// errorCutoff = -50h is past the durable row too.
	if _, err := repo.DeleteTraceSummariesOlderThan(ctx, now.Add(-50*time.Hour), now.Add(-50*time.Hour)); err != nil {
		t.Fatalf("delete at error cutoff: %v", err)
	}
	if got := summaries(); len(got) != 0 {
		t.Fatalf("after error cutoff want no summaries, got %v", got)
	}
}
