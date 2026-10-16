package repository

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"testing"
	"time"
)

func exemplarTraces(t *testing.T, repo *Repository) []string {
	t.Helper()
	var out []string
	err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		rows, err := db.Query(`SELECT trace_id FROM error_exemplars ORDER BY trace_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read exemplars: %v", err)
	}
	return out
}

func sampleTraces(t *testing.T, repo *Repository) []string {
	t.Helper()
	got, err := repo.QueryErrorSamples(SpanFilter{ProjectID: 1, Limit: 100})
	if err != nil {
		t.Fatalf("query error samples: %v", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range got {
		if !seen[s.TraceID] {
			seen[s.TraceID] = true
			out = append(out, s.TraceID)
		}
	}
	sort.Strings(out)
	return out
}

// The first error trace of a (service, operation, UTC day) becomes its
// exemplar. A second trace that day, an ok span and an error on another day or
// in another service are handled by that key alone.
func TestInsertErrorSamplesRecordsOneExemplarPerOperationPerDay(t *testing.T) {
	repo := setupTestDB(t)
	day1 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC).UnixMicro()
	day2 := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC).UnixMicro()

	if err := repo.InsertErrorSamples([]Span{
		tsSpan("A", "a1", "", "GET /pending", "web", "error", day1, 10),
		tsSpan("A", "a2", "", "page /app", "web", "ok", day1, 3_000_000),
		tsSpan("B", "b1", "", "GET /pending", "web", "error", day1+60_000_000, 10),
	}); err != nil {
		t.Fatalf("insert day 1: %v", err)
	}
	if err := repo.InsertErrorSamples([]Span{
		tsSpan("C", "c1", "", "GET /pending", "web", "error", day2, 10),
		tsSpan("D", "d1", "", "GET /pending", "api", "error", day2, 10),
	}); err != nil {
		t.Fatalf("insert day 2: %v", err)
	}

	if got, want := exemplarTraces(t, repo), []string{"A", "C", "D"}; !equalStrings(got, want) {
		t.Fatalf("exemplars = %v, want %v", got, want)
	}
}

// Past the error cutoff an exemplar keeps its error samples and its trace
// summary; every other error trace goes. Once its day falls before the
// exemplar cutoff, it goes too.
func TestErrorExemplarOutlivesErrorCutoff(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	start := now.Add(-10 * 24 * time.Hour)

	var spans []Span
	for _, id := range []string{"E1", "E2"} {
		s := tsSpan(id, id+"-s", "", "GET /pending", "web", "error", start.UnixMicro(), 10)
		s.IngestedAt = start
		spans = append(spans, s)
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("insert spans: %v", err)
	}
	if err := repo.InsertErrorSamples(spans); err != nil {
		t.Fatalf("insert error samples: %v", err)
	}
	if err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE error_samples SET sampled_at = ?`, start)
		return err
	}); err != nil {
		t.Fatalf("age samples: %v", err)
	}

	errorCutoff := now.Add(-7 * 24 * time.Hour)
	if _, err := repo.DeleteErrorSamplesOlderThan(ctx, errorCutoff); err != nil {
		t.Fatalf("delete samples: %v", err)
	}
	if _, err := repo.DeleteTraceSummariesOlderThan(ctx, now, errorCutoff); err != nil {
		t.Fatalf("delete summaries: %v", err)
	}
	if got := sampleTraces(t, repo); !equalStrings(got, []string{"E1"}) {
		t.Fatalf("samples after error cutoff = %v, want [E1]", got)
	}
	summaries, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1}, 0)
	if err != nil {
		t.Fatalf("search summaries: %v", err)
	}
	if len(summaries) != 1 || summaries[0].TraceID != "E1" {
		t.Fatalf("summaries after error cutoff = %+v, want only E1", summaries)
	}

	// A 30-day exemplar window still holds day -10.
	if n, err := repo.PruneErrorExemplars(ctx, now.Add(-30*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("prune at 30 days: n=%d err=%v, want 0", n, err)
	}
	// A 5-day window does not.
	if n, err := repo.PruneErrorExemplars(ctx, now.Add(-5*24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("prune at 5 days: n=%d err=%v, want 1", n, err)
	}
	if _, err := repo.DeleteErrorSamplesOlderThan(ctx, errorCutoff); err != nil {
		t.Fatalf("delete samples after prune: %v", err)
	}
	if _, err := repo.DeleteTraceSummariesOlderThan(ctx, now, errorCutoff); err != nil {
		t.Fatalf("delete summaries after prune: %v", err)
	}
	if got := sampleTraces(t, repo); len(got) != 0 {
		t.Fatalf("samples after prune = %v, want none", got)
	}
	if summaries, _ := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1}, 0); len(summaries) != 0 {
		t.Fatalf("summaries after prune = %+v, want none", summaries)
	}
}

// The delete must seek idx_error_samples_project. A global sampled_at filter
// has no index and scans every sample on each batch.
func TestDeleteErrorSamplesPlanSeeksProjectIndex(t *testing.T) {
	repo := setupTestDB(t)
	rows, err := repo.DB().Query("EXPLAIN QUERY PLAN "+deleteErrorSamplesSQL, 1, "2030-01-01", retentionDeleteBatch)
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
	if !strings.Contains(plan, "idx_error_samples_project") || strings.Contains(plan, "SCAN e") {
		t.Errorf("delete does not seek the project index:\n%s", plan)
	}
}

// Every project's old samples go, and recent ones stay.
func TestDeleteErrorSamplesOlderThanCoversEveryProject(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-10 * 24 * time.Hour)

	var spans []Span
	for _, c := range []struct {
		pid int64
		id  string
	}{{1, "P1-old"}, {2, "P2-old"}, {2, "P2-new"}} {
		s := tsSpan(c.id, c.id+"-s", "", "GET /x", "web", "error", now.UnixMicro(), 10)
		s.ProjectID = c.pid
		spans = append(spans, s)
	}
	if err := repo.InsertErrorSamples(spans); err != nil {
		t.Fatalf("insert error samples: %v", err)
	}
	if err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		if _, err := db.Exec(`DELETE FROM error_exemplars`); err != nil {
			return err
		}
		_, err := db.Exec(`UPDATE error_samples SET sampled_at = ? WHERE trace_id LIKE '%-old'`, old)
		return err
	}); err != nil {
		t.Fatalf("age samples: %v", err)
	}

	n, err := repo.DeleteErrorSamplesOlderThan(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("delete samples: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d samples, want 2", n)
	}
	var left []string
	if err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		rows, err := db.Query(`SELECT trace_id FROM error_samples ORDER BY trace_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			left = append(left, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("list samples: %v", err)
	}
	if !equalStrings(left, []string{"P2-new"}) {
		t.Errorf("samples left = %v, want [P2-new]", left)
	}
}
