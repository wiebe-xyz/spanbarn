package repository

import (
	"context"
	"testing"
	"time"
)

// insertTraceAt stores a one-span trace for project 1 with a controlled
// ingested_at (which flows into trace_summaries.ingested_at) and status.
func insertTraceAt(t *testing.T, repo *Repository, trace, status string, ingested time.Time) {
	t.Helper()
	s := tsSpan(trace, trace+"s1", "", "GET /"+trace, "svc", status, 1000, 100)
	s.IngestedAt = ingested
	if err := repo.InsertSpans([]Span{s}); err != nil {
		t.Fatalf("insert %s: %v", trace, err)
	}
}

func summaryTraceIDs(t *testing.T, repo *Repository) map[string]bool {
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

func TestEvictProjectTracesOlderThan(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-10 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	insertTraceAt(t, repo, "old1", "ok", old)      // evictable
	insertTraceAt(t, repo, "old2", "ok", old)      // evictable
	insertTraceAt(t, repo, "olderr", "error", old) // error → kept
	insertTraceAt(t, repo, "oldpin", "ok", old)    // pinned → kept
	insertTraceAt(t, repo, "new1", "ok", recent)   // newer than cutoff → kept
	if err := repo.PinTrace(ctx, 1, "oldpin", "keep"); err != nil {
		t.Fatalf("pin: %v", err)
	}

	evicted, err := repo.EvictProjectTracesOlderThan(ctx, 1, now.Add(-5*time.Hour))
	if err != nil {
		t.Fatalf("evict: %v", err)
	}
	if evicted != 2 {
		t.Fatalf("evicted = %d, want 2 (old1, old2)", evicted)
	}

	got := summaryTraceIDs(t, repo)
	for _, keep := range []string{"olderr", "oldpin", "new1"} {
		if !got[keep] {
			t.Errorf("%s should have survived eviction", keep)
		}
	}
	for _, gone := range []string{"old1", "old2"} {
		if got[gone] {
			t.Errorf("%s should have been evicted", gone)
		}
	}

	// Cascade: the evicted trace's spans are gone; a survivor's remain.
	if spans, _ := repo.QuerySpans(SpanFilter{ProjectID: 1, TraceID: "old1"}); len(spans) != 0 {
		t.Errorf("old1 spans not cascaded: %d remain", len(spans))
	}
	if spans, _ := repo.QuerySpans(SpanFilter{ProjectID: 1, TraceID: "new1"}); len(spans) != 1 {
		t.Errorf("new1 spans should remain, got %d", len(spans))
	}
}

func TestEvictProjectTracesByCount(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	// Five non-error traces oldest→newest, plus one old error trace.
	for i := 0; i < 5; i++ {
		insertTraceAt(t, repo, "n"+string(rune('0'+i)), "ok", base.Add(time.Duration(i)*time.Minute))
	}
	insertTraceAt(t, repo, "err", "error", base) // oldest, error → always kept

	// Fewer-than-N: nothing to evict.
	if _, ok, err := repo.ProjectNonErrorTraceCountCutoff(ctx, 1, 10); err != nil || ok {
		t.Fatalf("count cutoff with keepN>rows: ok=%v err=%v, want ok=false", ok, err)
	}

	// Keep newest 3 non-error → cutoff at the 3rd-newest (n2); evict n0, n1.
	cutoff, ok, err := repo.ProjectNonErrorTraceCountCutoff(ctx, 1, 3)
	if err != nil || !ok {
		t.Fatalf("count cutoff: ok=%v err=%v, want ok=true", ok, err)
	}
	if _, err := repo.EvictProjectTracesOlderThan(ctx, 1, cutoff); err != nil {
		t.Fatalf("evict: %v", err)
	}

	got := summaryTraceIDs(t, repo)
	for _, keep := range []string{"n2", "n3", "n4", "err"} {
		if !got[keep] {
			t.Errorf("%s should have survived (newest 3 non-error + error)", keep)
		}
	}
	for _, gone := range []string{"n0", "n1"} {
		if got[gone] {
			t.Errorf("%s should have been evicted", gone)
		}
	}
}
