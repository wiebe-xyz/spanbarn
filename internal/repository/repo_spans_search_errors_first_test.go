package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedMixedSummaries stores 120 summaries over two projects, every fifth with
// an error, ingested a minute apart, so error and other rows interleave.
func seedMixedSummaries(t *testing.T, repo *Repository, base time.Time) {
	t.Helper()
	if err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		for i := 0; i < 120; i++ {
			if _, err := db.Exec(`INSERT INTO trace_summaries (project_id, trace_id, ingested_at, has_error, root_service)
				VALUES (?, ?, ?, ?, ?)`, 1+i%2, fmt.Sprintf("t%03d", i), base.Add(time.Duration(i)*time.Minute), i%5 == 0, "svc"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func traceIDs(rows []TraceSummaryRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.TraceID
	}
	return out
}

// The errors-first list returns the rows the full ordering would: error
// traces newest first, then the rest newest first, on every page and filter.
func TestErrorsFirstListMatchesTheFullOrdering(t *testing.T) {
	repo := setupTestDB(t)
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seedMixedSummaries(t, repo, base)

	for name, f := range map[string]SpanFilter{
		"all projects": {},
		"one project":  {ProjectID: 1},
		"a window":     {From: base.Add(30 * time.Minute), To: base.Add(90 * time.Minute)},
		"a service":    {ProjectID: 2, Service: "svc"},
		"errors only":  {Status: "error"},
		"ok only":      {Status: "ok"},
	} {
		t.Run(name, func(t *testing.T) {
			f.Limit = 1000
			want, err := repo.SearchTraceSummaries(f, 0)
			if err != nil {
				t.Fatal(err)
			}
			wantRows := sortedErrorsFirst(want)
			for _, page := range []struct{ limit, offset int }{{10, 0}, {10, 10}, {7, 3}, {25, 20}, {50, 100}} {
				p := f
				p.SortErrorsFirst, p.Limit, p.Offset = true, page.limit, page.offset
				got, err := repo.SearchTraceSummaries(p, 0)
				if err != nil {
					t.Fatalf("page %+v: %v", page, err)
				}
				lo, hi := min(page.offset, len(wantRows)), min(page.offset+page.limit, len(wantRows))
				if g, w := strings.Join(traceIDs(got), ","), strings.Join(traceIDs(wantRows[lo:hi]), ","); g != w {
					t.Errorf("page %+v:\n got %s\nwant %s", page, g, w)
				}
			}
		})
	}
}

// sortedErrorsFirst orders rows newest first, which SearchTraceSummaries does
// without SortErrorsFirst, then moves the error rows to the front.
func sortedErrorsFirst(newestFirst []TraceSummaryRow) []TraceSummaryRow {
	var errs, rest []TraceSummaryRow
	for _, r := range newestFirst {
		if r.HasError {
			errs = append(errs, r)
		} else {
			rest = append(rest, r)
		}
	}
	return append(errs, rest...)
}

// The page is bounded by the offset and limit in each group, so no group is
// read in full and sorted: both groups walk an index in order, and the one
// temp B-tree sorts the union of at most 2*(offset+limit) rows.
func TestErrorsFirstListReadsIndexesInOrder(t *testing.T) {
	repo := setupTestDB(t)
	for name, tc := range map[string]struct {
		where string
		args  []any
		want  []string
	}{
		"all projects": {"", nil, []string{"USING INDEX idx_trace_summaries_error", "USING INDEX idx_trace_summaries_clean"}},
		"one project":  {" WHERE project_id = ?", []any{1, 1}, []string{"USING INDEX idx_trace_summaries_list (project_id=?)"}},
	} {
		t.Run(name, func(t *testing.T) {
			plan := queryPlan(t, repo, errorsFirstSummaryQuery(tc.where, 50, 0), tc.args...)
			for _, want := range tc.want {
				if !strings.Contains(plan, want) {
					t.Errorf("plan lacks %q:\n%s", want, plan)
				}
			}
			if strings.Contains(plan, "SCAN trace_summaries\n") {
				t.Errorf("a group scans the table in no index order:\n%s", plan)
			}
			if n := strings.Count(plan, "USE TEMP B-TREE"); n != 1 {
				t.Errorf("%d temp B-trees, want the one over the union:\n%s", n, plan)
			}
		})
	}
}
