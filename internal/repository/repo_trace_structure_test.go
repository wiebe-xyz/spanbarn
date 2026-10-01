package repository

import (
	"context"
	"strings"
	"testing"
	"time"
)

func summaryRow(t *testing.T, repo *Repository, trace string) (hasRoot *bool, orphans, spans int, rootName string) {
	t.Helper()
	rows, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1}, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, r := range rows {
		if r.TraceID == trace {
			return r.HasRoot, r.OrphanCount, r.SpanCount, r.RootName
		}
	}
	t.Fatalf("trace %s has no summary", trace)
	return
}

func boolPtr(v bool) *bool { return &v }

func TestTraceStructureSetAtIngest(t *testing.T) {
	repo := setupTestDB(t)

	// Healthy trace: root plus child.
	_ = repo.InsertSpans([]Span{
		tsSpan("ok", "o1", "", "GET /a", "web", "ok", 1, 10),
		tsSpan("ok", "o2", "o1", "SELECT", "db", "ok", 2, 3),
	})
	// Rootless trace: only spans with a missing parent.
	_ = repo.InsertSpans([]Span{
		tsSpan("orph", "p1", "missing", "POST /presign", "web", "ok", 1, 10),
		tsSpan("orph", "p2", "p1", "SELECT", "db", "ok", 2, 3),
	})

	hr, orphans, n, name := summaryRow(t, repo, "ok")
	if hr == nil || !*hr || orphans != 0 || n != 2 || name != "GET /a" {
		t.Errorf("ok trace: hasRoot=%v orphans=%d spans=%d root=%q", hr, orphans, n, name)
	}
	hr, orphans, n, name = summaryRow(t, repo, "orph")
	if hr == nil || *hr || orphans != 1 || n != 2 || name != "" {
		t.Errorf("orphan trace: hasRoot=%v orphans=%d spans=%d root=%q", hr, orphans, n, name)
	}

	rootless, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1, HasRoot: boolPtr(false)}, 0)
	if err != nil || len(rootless) != 1 || rootless[0].TraceID != "orph" {
		t.Fatalf("has_root=false filter: %v %+v", err, rootless)
	}
	withRoot, _ := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1, HasRoot: boolPtr(true)}, 0)
	if len(withRoot) != 1 || withRoot[0].TraceID != "ok" {
		t.Fatalf("has_root=true filter: %+v", withRoot)
	}
	orphaned, _ := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1, HasOrphans: true}, 0)
	if len(orphaned) != 1 || orphaned[0].TraceID != "orph" {
		t.Fatalf("orphans filter: %+v", orphaned)
	}
}

// A parent that arrives in a later batch resolves the orphan, and a late root
// restores has_root and the root fields.
func TestTraceStructureRecomputedOnLateSpans(t *testing.T) {
	repo := setupTestDB(t)

	_ = repo.InsertSpans([]Span{tsSpan("late", "c1", "r1", "child", "web", "ok", 2, 3)})
	hr, orphans, _, _ := summaryRow(t, repo, "late")
	if hr == nil || *hr || orphans != 1 {
		t.Fatalf("before root: hasRoot=%v orphans=%d", hr, orphans)
	}

	// The parent arrives, itself a child of a still-missing span.
	_ = repo.InsertSpans([]Span{tsSpan("late", "r1", "gone", "mid", "web", "ok", 1, 9)})
	_, orphans, n, _ := summaryRow(t, repo, "late")
	if orphans != 1 || n != 2 {
		t.Fatalf("after parent: orphans=%d spans=%d, want 1 and 2", orphans, n)
	}

	// The true root lands last.
	_ = repo.InsertSpans([]Span{tsSpan("late", "gone", "", "GET /late", "web", "ok", 0, 20)})
	hr, orphans, n, name := summaryRow(t, repo, "late")
	if hr == nil || !*hr || orphans != 0 || n != 3 || name != "GET /late" {
		t.Fatalf("after root: hasRoot=%v orphans=%d spans=%d root=%q", hr, orphans, n, name)
	}
}

// Legacy rows (NULL structure) match no structural filter until backfilled.
func TestBackfillTraceStructure(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	_ = repo.InsertSpans([]Span{
		tsSpan("a", "a1", "", "root", "web", "ok", 1, 1),
		tsSpan("b", "b1", "nope", "child", "web", "ok", 1, 1),
	})
	// An error trace whose spans are all gone keeps listing; it is also legacy.
	_ = repo.InsertSpans([]Span{tsSpan("c", "c1", "", "gone root", "web", "error", 1, 1)})
	if _, err := repo.DB().Exec(`DELETE FROM spans WHERE trace_id = 'c'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB().Exec(`UPDATE trace_summaries SET has_root = NULL, orphan_count = NULL`); err != nil {
		t.Fatal(err)
	}

	got, _ := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1, HasRoot: boolPtr(false)}, 0)
	if len(got) != 0 {
		t.Fatalf("unfilled rows must not match has_root=false: %+v", got)
	}

	n, more, err := repo.BackfillTraceStructure(ctx, 2)
	if err != nil || n != 2 || !more {
		t.Fatalf("first pass: n=%d more=%v err=%v", n, more, err)
	}
	n, more, err = repo.BackfillTraceStructure(ctx, 100)
	if err != nil || n != 1 || more {
		t.Fatalf("second pass: n=%d more=%v err=%v", n, more, err)
	}

	if hr, _, _, _ := summaryRow(t, repo, "a"); hr == nil || !*hr {
		t.Errorf("a should have a root, got %v", hr)
	}
	if hr, o, _, _ := summaryRow(t, repo, "b"); hr == nil || *hr || o != 1 {
		t.Errorf("b should be rootless with 1 orphan, got %v / %d", hr, o)
	}
	// Spans gone: the summary's own root_name is the only evidence.
	if hr, o, _, _ := summaryRow(t, repo, "c"); hr == nil || !*hr || o != 0 {
		t.Errorf("c should settle from root_name, got %v / %d", hr, o)
	}

	if n, more, _ = repo.BackfillTraceStructure(ctx, 100); n != 0 || more {
		t.Errorf("nothing left to fill, got n=%d more=%v", n, more)
	}
}

// Deleting the spans a retention pass aggregates refreshes the surviving error
// summary, and clears a root name whose root span is gone.
func TestDeleteSpansByMaxIDRefreshesErrorSummaries(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	_ = repo.InsertSpans([]Span{
		tsSpan("e", "root", "", "GET /e", "web", "ok", 1, 10),
		tsSpan("e", "kid", "root", "SELECT", "db", "error", 2, 3),
	})
	// Evict only the root (the lowest id).
	var rootID int64
	if err := repo.DB().QueryRow(`SELECT id FROM spans WHERE span_id = 'root'`).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeleteSpansByMaxIDRefreshing(ctx, rootID); err != nil {
		t.Fatal(err)
	}
	hr, orphans, n, name := summaryRow(t, repo, "e")
	if hr == nil || *hr || orphans != 1 || n != 1 || name != "" {
		t.Fatalf("after root eviction: hasRoot=%v orphans=%d spans=%d root=%q", hr, orphans, n, name)
	}
}

func TestTraceHealthQueries(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	_ = repo.InsertSpans([]Span{
		// Two single-span root traces with the same name.
		tsSpan("s1", "x1", "", "job.run", "worker", "ok", 1, 1),
		tsSpan("s2", "x2", "", "job.run", "worker", "ok", 1, 1),
		// Healthy trace.
		tsSpan("h", "h1", "", "GET /h", "web", "ok", 1, 1),
		tsSpan("h", "h2", "h1", "SELECT", "db", "ok", 1, 1),
		// Two traces with a server span whose parent was never ingested.
		tsSpan("o1", "y1", "ghost", "POST /presign", "web", "ok", 1, 1),
		tsSpan("o2", "y2", "ghost", "POST /presign", "web", "ok", 1, 1),
	})
	w := HealthWindow{ProjectID: 1, From: time.Now().UTC().Add(-time.Hour), To: time.Now().UTC().Add(time.Hour)}

	orph, err := repo.QueryOrphanSpanGroups(ctx, w)
	if err != nil || len(orph) != 1 || orph[0].Name != "POST /presign" || orph[0].Count != 2 || orph[0].SampleTraceID == "" {
		t.Fatalf("orphans: %v %+v", err, orph)
	}
	single, err := repo.QuerySingleSpanTraceGroups(ctx, w)
	if err != nil || len(single) != 2 || single[0].Count != 2 || single[1].Count != 2 {
		t.Fatalf("single-span: %v %+v", err, single)
	}
	names, err := repo.QuerySpanNameSummary(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]SpanNameSummary{}
	for _, n := range names {
		by[n.Name] = n
	}
	if by["job.run"].Count != 2 || by["job.run"].RootCount != 2 {
		t.Errorf("job.run: %+v", by["job.run"])
	}
	if by["SELECT"].Count != 1 || by["SELECT"].RootCount != 0 {
		t.Errorf("SELECT: %+v", by["SELECT"])
	}
	if n, err := repo.RootlessTraceCount(ctx, w); err != nil || n != 2 {
		t.Errorf("rootless count = %d, %v", n, err)
	}

	// Other project and a window in the past see nothing.
	if got, _ := repo.QueryOrphanSpanGroups(ctx, HealthWindow{ProjectID: 2, From: w.From, To: w.To}); len(got) != 0 {
		t.Errorf("project scope leaked: %+v", got)
	}
	past := HealthWindow{ProjectID: 1, From: time.Now().Add(-48 * time.Hour), To: time.Now().Add(-47 * time.Hour)}
	if got, _ := repo.QuerySpanNameSummary(ctx, past); len(got) != 0 {
		t.Errorf("window ignored: %+v", got)
	}
}

// The orphan check must seek an index for the parent lookup and for the outer
// window scan, never scan the whole spans table.
func TestOrphanQueryPlanUsesIndexes(t *testing.T) {
	repo := setupTestDB(t)
	rows, err := repo.DB().Query(`EXPLAIN QUERY PLAN
		SELECT s.name FROM spans s
		WHERE s.project_id = 1 AND s.ingested_at >= '2026-01-01' AND s.ingested_at <= '2027-01-01'
		  AND s.parent_span_id IS NOT NULL AND s.parent_span_id != ''
		  AND NOT EXISTS (SELECT 1 FROM spans p
		                  WHERE p.trace_id = s.trace_id AND p.project_id = s.project_id
		                    AND p.span_id = s.parent_span_id)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if strings.Contains(plan, "SCAN p") || strings.Contains(plan, "SCAN s") {
		t.Errorf("full table scan in plan:\n%s", plan)
	}
}
