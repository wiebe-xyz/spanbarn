package repository

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The window read seeks idx_error_samples_project from the start of a
// project's samples and from a cursor. A bare `sampled_at < ?` has no index to
// use and scanned every sample (42s on prod's 527k rows).
func TestErrorSampleWindowPlanSeeksProjectIndex(t *testing.T) {
	repo := setupTestDB(t)
	for _, resume := range []bool{false, true} {
		rows, err := repo.DB().Query("EXPLAIN QUERY PLAN "+errorSampleWindowSQL(resume),
			1, "2030-01-01", "2029-01-01", 0, retentionDeleteBatch)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail + "\n"
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, "SEARCH e USING INDEX idx_error_samples_project (project_id=? AND ") ||
			strings.Contains(plan, "SCAN e") || strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("resume %v: window does not walk the project index in order:\n%s", resume, plan)
		}
	}
}

// seedErrorSampleWindows inserts, for project 1, protected samples of the
// exemplar trace P copied before deletable samples D0..D(deletable-1), all
// before the cutoff, and one recent sample R. It returns the cutoff.
func seedErrorSampleWindows(t *testing.T, repo *Repository, protected, deletable int) time.Time {
	t.Helper()
	now := time.Now().UTC()
	var spans []Span
	for i := range protected {
		spans = append(spans, tsSpan("P", fmt.Sprintf("p%d", i), "", "GET /x", "web", "error", now.UnixMicro(), 10))
	}
	for i := range deletable {
		spans = append(spans, tsSpan(fmt.Sprintf("D%d", i), "d", "", "GET /x", "web", "error", now.UnixMicro(), 10))
	}
	spans = append(spans, tsSpan("R", "r", "", "GET /x", "web", "error", now.UnixMicro(), 10))
	if err := repo.InsertErrorSamples(spans); err != nil {
		t.Fatalf("insert error samples: %v", err)
	}
	old := now.Add(-10 * 24 * time.Hour)
	if err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM error_exemplars`, nil},
			{`INSERT INTO error_exemplars (project_id, service, name, day, trace_id) VALUES (1, 'web', 'GET /x', '2026-01-01', 'P')`, nil},
			{`UPDATE error_samples SET sampled_at = ? WHERE trace_id = 'P'`, []any{old.Add(-time.Hour)}},
			{`UPDATE error_samples SET sampled_at = ? WHERE trace_id LIKE 'D%'`, []any{old}},
		} {
			if _, err := db.Exec(stmt.sql, stmt.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("age samples: %v", err)
	}
	return now.Add(-7 * 24 * time.Hour)
}

// errorSampleTraceCounts counts the samples left per trace.
func errorSampleTraceCounts(t *testing.T, repo *Repository) map[string]int {
	t.Helper()
	out := map[string]int{}
	if err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		rows, err := db.Query(`SELECT trace_id, count(*) FROM error_samples GROUP BY trace_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				return err
			}
			out[id] = n
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	return out
}

// A window holds only protected samples when an exemplar trace has more than
// a window of them before the deletable ones. The pass must walk past it and
// delete the rest, on the writer alone and with the window read on the read
// pool.
func TestDeleteErrorSamplesWalksPastProtectedWindows(t *testing.T) {
	const protected, deletable = retentionDeleteBatch + 5, 10
	for _, readPool := range []bool{false, true} {
		t.Run(fmt.Sprintf("read pool %v", readPool), func(t *testing.T) {
			repo := setupTestDB(t)
			if readPool {
				path := filepath.Join(t.TempDir(), "spanbarn.db")
				repo = openTestStorage(t, path).Repository()
				ro, err := OpenReadDB(path, 2, 0)
				if err != nil {
					t.Fatalf("open read db: %v", err)
				}
				t.Cleanup(func() { ro.Close() })
				repo.SetLogTrimReads(ro.DB)
			}
			cutoff := seedErrorSampleWindows(t, repo, protected, deletable)

			n, err := repo.DeleteErrorSamplesOlderThan(context.Background(), cutoff)
			if err != nil {
				t.Fatalf("delete samples: %v", err)
			}
			if n != deletable {
				t.Errorf("deleted %d samples, want %d", n, deletable)
			}
			got := errorSampleTraceCounts(t, repo)
			if len(got) != 2 || got["P"] != protected || got["R"] != 1 {
				t.Errorf("samples left per trace = %v, want P:%d R:1", got, protected)
			}
		})
	}
}

// The DELETE checks each listed row again: a trace that became an exemplar
// after the window was read keeps its sample, and a listed row id that now
// holds a sample newer than the cutoff stays.
func TestDeleteErrorSampleRowsChecksEachRowAgain(t *testing.T) {
	repo := setupTestDB(t)
	cutoff := seedErrorSampleWindows(t, repo, 0, 2)
	ids := map[string]int64{}
	if err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		rows, err := db.Query(`SELECT trace_id, rowid FROM error_samples`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var rowid int64
			if err := rows.Scan(&id, &rowid); err != nil {
				return err
			}
			ids[id] = rowid
		}
		if err := rows.Err(); err != nil {
			return err
		}
		_, err = db.Exec(`INSERT INTO error_exemplars (project_id, service, name, day, trace_id) VALUES (1, 'web', 'GET /x', '2026-01-02', 'D0')`)
		return err
	}); err != nil {
		t.Fatalf("read rows: %v", err)
	}

	n, err := deleteErrorSampleRows(context.Background(), repo.writer(FamilySpans), 1, cutoff,
		[]int64{ids["D0"], ids["D1"], ids["R"]})
	if err != nil {
		t.Fatalf("delete rows: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d rows, want 1", n)
	}
	if got := errorSampleTraceCounts(t, repo); len(got) != 2 || got["D0"] != 1 || got["R"] != 1 {
		t.Errorf("samples left per trace = %v, want D0:1 R:1", got)
	}
}
