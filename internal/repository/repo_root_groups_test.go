package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// SQLite's bin of a duration must equal spanhist.Index, or a histogram folded
// in SQL and one folded in Go disagree. The sweep covers every value up to
// 5000us and each power of two with its neighbours, where log2 is exact.
func TestSpanhistIndexMatchesSQLite(t *testing.T) {
	repo := setupTestDB(t)
	values := []int64{-3}
	for v := int64(0); v <= 5000; v++ {
		values = append(values, v)
	}
	for k := 1; k < 50; k++ {
		p := int64(1) << k
		values = append(values, p-2, p-1, p, p+1)
	}
	for _, v := range values {
		var got int
		if err := repo.DB().QueryRow(`SELECT `+spanhist.IndexSQL("?"), v).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got, want := spanhist.Clamp(got), spanhist.Index(v); got != want {
			t.Fatalf("bin of %dus: SQLite %d, Go %d", v, got, want)
		}
	}
}

// SQLite folds root spans into bins per group; the histogram gives the same
// percentiles a sort of the raw durations gave, child spans stay out, and the
// all-projects view sums sample weights across projects.
func TestQueryRootSpanGroupsFoldsInSQL(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 100; i++ {
		dashSpan(t, repo, fmt.Sprintf("r%d", i), "", "GET /", "web", `{}`, int64(i)*1000, at)
		dashSpan(t, repo, fmt.Sprintf("c%d", i), fmt.Sprintf("r%d", i), "db.query", "web", `{}`, 5, at)
	}
	if _, err := repo.writer(FamilySpans).Exec(`UPDATE spans SET sample_weight = 10 WHERE COALESCE(parent_span_id, '') = ''`); err != nil {
		t.Fatal(err)
	}
	other := Span{ProjectID: 2, TraceID: "t-x", SpanID: "x", Name: "GET /", Service: "web", Kind: "server",
		Status: "error", StartTimeUs: at.UnixMicro(), DurationUs: 7000, Attributes: `{}`, Events: "[]", SampleWeight: 1}
	if err := repo.InsertSpans([]Span{other}); err != nil {
		t.Fatal(err)
	}

	one, err := repo.QueryRootSpanGroups(context.Background(), SpanFilter{ProjectID: 1})
	if err != nil || len(one) != 1 {
		t.Fatalf("project 1 groups = %+v, %v", one, err)
	}
	g := one[0]
	if g.Count != 100 || g.ErrorCount != 0 || g.Weights != (SpanWeights{Count: 1000, Spans: 100}) {
		t.Errorf("project 1 group = count %d, errors %d, weights %+v", g.Count, g.ErrorCount, g.Weights)
	}
	for p, want := range map[float64]int64{50: 50_000, 95: 95_000, 99: 99_000} {
		if got := g.Hist.Percentile(p); got != want {
			t.Errorf("p%v = %d, want %d", p, got, want)
		}
	}

	all, err := repo.QueryRootSpanGroups(context.Background(), SpanFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("all-projects groups = %+v, %v", all, err)
	}
	if a := all[0]; a.Count != 101 || a.ErrorCount != 1 || a.Weights != (SpanWeights{Count: 1001, Spans: 101}) {
		t.Errorf("all-projects group = count %d, errors %d, weights %+v", a.Count, a.ErrorCount, a.Weights)
	}
}

// The all-projects query reads the dashboard index, whose leading ingested_at
// gives a range scan without a project, and one project reads the root-only
// partial index.
func TestQueryRootSpanGroupsPinsItsIndex(t *testing.T) {
	repo := setupTestDB(t)
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for f, want := range map[*SpanFilter]string{
		{From: from, To: from.Add(time.Hour), Status: "ok", MinDuration: 10, ExcludeOperations: []string{"x"}}: "USING INDEX idx_spans_dashboard (ingested_at>? AND ingested_at<?)",
		{ProjectID: 5, From: from, To: from.Add(time.Hour)}:                                                    "USING INDEX idx_spans_root_ingested (project_id=? AND ingested_at>? AND ingested_at<?)",
	} {
		where, args := f.rootSpanGroupWhere()
		q := fmt.Sprintf(`EXPLAIN QUERY PLAN SELECT name, service, COUNT(*), SUM(COALESCE(sample_weight, 0)) FROM %s WHERE %s GROUP BY name, service`,
			rootSpanGroupSource(*f), strings.Join(where, " AND "))
		rows, err := repo.DB().Query(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		if joined := strings.Join(plan, " | "); !strings.Contains(joined, want) {
			t.Errorf("project %d plan %q lacks %q", f.ProjectID, joined, want)
		}
	}
}
