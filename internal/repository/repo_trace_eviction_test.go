package repository

import (
	"context"
	"testing"
	"time"
)

// summaryIDs returns the listed traces of project 1 with their spansAvailable flag.
func summaryIDs(t *testing.T, repo *Repository) map[string]bool {
	t.Helper()
	rows, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1}, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.TraceID] = r.SpansAvailable
	}
	return got
}

// A summary whose spans are gone from both spans and error_samples lists with
// spansAvailable false; one whose spans moved to error_samples stays available.
func TestTraceListFlagsEvictedSpans(t *testing.T) {
	repo := setupTestDB(t)
	if err := repo.InsertSpans([]Span{
		tsSpan("live", "l1", "", "GET /a", "web", "ok", 1, 10),
		tsSpan("gone", "g1", "", "GET /b", "web", "ok", 1, 10),
		tsSpan("kept", "k1", "", "GET /c", "web", "error", 1, 10),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertErrorSamples([]Span{tsSpan("kept", "k1", "", "GET /c", "web", "error", 1, 10)}); err != nil {
		t.Fatal(err)
	}
	// Evict spans behind the summaries' back, as the disk reclaim does.
	if _, err := repo.writer(FamilySpans).Exec(`DELETE FROM spans WHERE trace_id IN ('gone', 'kept')`); err != nil {
		t.Fatal(err)
	}

	got := summaryIDs(t, repo)
	want := map[string]bool{"live": true, "gone": false, "kept": true}
	for id, w := range want {
		avail, ok := got[id]
		if !ok {
			t.Fatalf("trace %s not listed: %v", id, got)
		}
		if avail != w {
			t.Errorf("trace %s spansAvailable = %v, want %v", id, avail, w)
		}
	}
}

// The retention drain deletes an error or durable summary once its last span
// is gone and none reached error_samples, and keeps it while error_samples
// still holds a span.
func TestDeleteSpansByMaxIDDropsSpanlessSummaries(t *testing.T) {
	repo := setupTestDB(t)
	durable := tsSpan("dur", "d1", "", "sweep", "cron", "ok", 1, 10)
	durable.Durable = true
	durableCopied := tsSpan("durc", "c1", "", "sweep", "cron", "ok", 1, 10)
	durableCopied.Durable = true
	if err := repo.InsertSpans([]Span{
		tsSpan("err", "e1", "", "GET /e", "web", "error", 1, 10),
		tsSpan("errc", "x1", "", "GET /x", "web", "error", 1, 10),
		durable, durableCopied,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertErrorSamples([]Span{
		tsSpan("errc", "x1", "", "GET /x", "web", "error", 1, 10), durableCopied,
	}); err != nil {
		t.Fatal(err)
	}
	var maxID int64
	if err := repo.DB().QueryRow(`SELECT MAX(id) FROM spans`).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeleteSpansByMaxIDRefreshing(context.Background(), maxID); err != nil {
		t.Fatal(err)
	}

	got := summaryIDs(t, repo)
	for _, id := range []string{"err", "dur"} {
		if _, ok := got[id]; ok {
			t.Errorf("summary %s outlived its spans", id)
		}
	}
	for _, id := range []string{"errc", "durc"} {
		if !got[id] {
			t.Errorf("summary %s with spans in error_samples = %v listed, want listed and available", id, got)
		}
	}
}

// Boring expiry deletes the summary of a trace with its last span, and keeps
// and refreshes the summary of a trace that still has spans kept longer.
func TestDeleteExpiredBoringSpansPrunesSummaries(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()
	past := now.Add(-time.Hour)

	expiring := func(s Span) Span { s.ExpiresAt = &past; return s }
	if err := repo.InsertSpans([]Span{
		expiring(tsSpan("mixed", "m1", "", "POST /contact", "web", "ok", 1, 10)),
		tsSpan("mixed", "m2", "m1", "send", "mail", "error", 2, 5),
		expiring(tsSpan("orphan", "o1", "", "DELETE /u", "web", "ok", 1, 10)),
	}); err != nil {
		t.Fatal(err)
	}
	// The orphan's summary was kept indefinitely by a span that is gone already.
	if _, err := repo.writer(FamilySpans).Exec(`UPDATE trace_summaries SET expires_at = NULL WHERE trace_id = 'orphan'`); err != nil {
		t.Fatal(err)
	}

	n, err := repo.DeleteExpiredBoringSpans(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("deleted %d spans, want 2", n)
	}
	if _, ok := summaryIDs(t, repo)["orphan"]; ok {
		t.Error("summary of a trace with no spans left is still listed")
	}
	hr, orphans, spans, root := summaryRow(t, repo, "mixed")
	if hr == nil || *hr || orphans != 1 || spans != 1 || root != "" {
		t.Errorf("mixed after expiry: hasRoot=%v orphans=%d spans=%d root=%q", hr, orphans, spans, root)
	}
}
