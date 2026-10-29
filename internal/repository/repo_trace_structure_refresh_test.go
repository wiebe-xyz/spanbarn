package repository

import (
	"fmt"
	"testing"
)

// legacyTraceStructureSQL is the refresh before it read the trace once: three
// subqueries over spans and a parent seek per span. The single-pass statement
// must give the same span_count, has_root and orphan_count.
const legacyTraceStructureSQL = `SELECT
	(SELECT COUNT(*) FROM spans WHERE project_id = ?1 AND trace_id = ?2),
	EXISTS (SELECT 1 FROM spans WHERE project_id = ?1 AND trace_id = ?2
		AND (parent_span_id IS NULL OR parent_span_id = '')),
	(SELECT COUNT(*) FROM spans s
		WHERE s.project_id = ?1 AND s.trace_id = ?2
		  AND s.parent_span_id IS NOT NULL AND s.parent_span_id != ''
		  AND NOT EXISTS (SELECT 1 FROM spans p
		                  WHERE p.span_id = s.parent_span_id
		                    AND p.project_id = ?1 AND p.trace_id = ?2))`

type traceStructure struct {
	spans, orphans int
	hasRoot        bool
}

func legacyStructure(t *testing.T, repo *Repository, trace string) traceStructure {
	t.Helper()
	var s traceStructure
	if err := repo.DB().QueryRow(legacyTraceStructureSQL, 1, trace).Scan(&s.spans, &s.hasRoot, &s.orphans); err != nil {
		t.Fatal(err)
	}
	return s
}

func storedStructure(t *testing.T, repo *Repository, trace string) traceStructure {
	t.Helper()
	var s traceStructure
	err := repo.DB().QueryRow(`SELECT span_count, has_root, orphan_count FROM trace_summaries
		WHERE project_id = 1 AND trace_id = ?`, trace).Scan(&s.spans, &s.hasRoot, &s.orphans)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func insertInBatches(t *testing.T, repo *Repository, spans []Span, size int) {
	t.Helper()
	for i := 0; i < len(spans); i += size {
		if err := repo.InsertSpans(spans[i:min(i+size, len(spans))]); err != nil {
			t.Fatal(err)
		}
	}
}

// bigTrace is a 3000-span tree under one root in which every 100th span points
// at a parent that never arrives, plus two spans whose parent span_id exists
// only in another trace or another project. Those are orphans too.
func bigTrace() []Span {
	const n = 3000
	spans := make([]Span, 0, n+4)
	for i := 0; i < n; i++ {
		parent := ""
		switch {
		case i > 0 && i%100 == 0:
			parent = fmt.Sprintf("missing-%d", i)
		case i > 0:
			parent = fmt.Sprintf("s%d", (i-1)/2)
		}
		spans = append(spans, tsSpan("big", fmt.Sprintf("s%d", i), parent, "op", "svc", "ok", int64(i), 10))
	}
	other := tsSpan("other", "ghost", "", "op", "svc", "ok", 1, 10)
	elsewhere := tsSpan("big", "p2span", "", "op", "svc", "ok", 1, 10)
	elsewhere.ProjectID = 2
	return append(spans, other, elsewhere,
		tsSpan("big", "x-ghost", "ghost", "op", "svc", "ok", 1, 10),
		tsSpan("big", "x-p2", "p2span", "op", "svc", "ok", 1, 10))
}

func TestRefreshTraceStructureMatchesLegacyQueries(t *testing.T) {
	repo := setupTestDB(t)
	insertInBatches(t, repo, bigTrace(), 500)

	want := traceStructure{spans: 3002, orphans: 31, hasRoot: true}
	if got := legacyStructure(t, repo, "big"); got != want {
		t.Fatalf("legacy queries on the fixture = %+v, want %+v", got, want)
	}
	if got := storedStructure(t, repo, "big"); got != want {
		t.Errorf("stored structure = %+v, want %+v", got, want)
	}
}

// A trace whose parents arrive in a later batch: the refresh after each batch
// matches the legacy queries, from orphaned and rootless to complete.
func TestRefreshTraceStructureMatchesLegacyOnLateParents(t *testing.T) {
	repo := setupTestDB(t)
	batches := [][]Span{
		{tsSpan("L", "l2", "l1", "op", "svc", "ok", 2, 10), tsSpan("L", "l3", "l1", "op", "svc", "ok", 3, 10),
			tsSpan("R", "r2", "r1", "op", "svc", "ok", 2, 10), tsSpan("R", "r1", "gone", "op", "svc", "ok", 1, 10)},
		{tsSpan("L", "l1", "", "op", "svc", "ok", 1, 10)},
	}
	wants := []map[string]traceStructure{
		{"L": {spans: 2, orphans: 2}, "R": {spans: 2, orphans: 1}},
		{"L": {spans: 3, hasRoot: true}, "R": {spans: 2, orphans: 1}},
	}
	for i, b := range batches {
		if err := repo.InsertSpans(b); err != nil {
			t.Fatal(err)
		}
		for trace, want := range wants[i] {
			if got := legacyStructure(t, repo, trace); got != want {
				t.Fatalf("batch %d trace %s: legacy = %+v, want %+v", i, trace, got, want)
			}
			if got := storedStructure(t, repo, trace); got != want {
				t.Errorf("batch %d trace %s: stored = %+v, want %+v", i, trace, got, want)
			}
		}
	}
}
