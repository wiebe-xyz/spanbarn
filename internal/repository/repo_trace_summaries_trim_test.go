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

// The window read walks idx_trace_summaries_list from the start of a
// project's summaries and from a cursor, with no sort. The batched DELETE it
// replaces scanned the whole table in rowid order on every batch.
func TestTraceSummaryWindowPlanSeeksListIndex(t *testing.T) {
	repo := setupTestDB(t)
	for _, resume := range []bool{false, true} {
		rows, err := repo.DB().Query("EXPLAIN QUERY PLAN "+traceSummaryWindowSQL(resume),
			1, "2030-01-01", "2029-01-01", 0, retentionDeleteBatch, "2030-01-01", "2029-01-01")
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
		if !strings.Contains(plan, "SEARCH t USING INDEX idx_trace_summaries_list (project_id=? AND ") ||
			strings.Contains(plan, "SCAN t") || strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("resume %v: window does not walk the list index in order:\n%s", resume, plan)
		}
	}
}

// insertTraceSummaries inserts n summaries of project 1 named prefix0..,
// all ingested at one time, so a window boundary falls between rows of equal
// ingested_at.
func insertTraceSummaries(t *testing.T, repo *Repository, prefix string, n int, ingested time.Time, hasError bool) {
	t.Helper()
	if err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		_, err := db.Exec(`WITH RECURSIVE seq(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM seq WHERE i + 1 < ?1)
			INSERT INTO trace_summaries (project_id, trace_id, ingested_at, has_error)
			SELECT 1, ?2 || i, ?3, ?4 FROM seq`, n, prefix, ingested, hasError)
		return err
	}); err != nil {
		t.Fatalf("insert summaries %s: %v", prefix, err)
	}
}

// summaryPrefixCounts counts the summaries left per trace-id prefix letter.
func summaryPrefixCounts(t *testing.T, repo *Repository) map[string]int {
	t.Helper()
	out := map[string]int{}
	rows, err := repo.writer(FamilySpans).Query(`SELECT substr(trace_id, 1, 1), count(*) FROM trace_summaries GROUP BY 1`)
	if err != nil {
		t.Fatalf("count summaries: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			t.Fatal(err)
		}
		out[p] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedTraceSummaryWindows inserts, oldest first: an exemplar error trace X
// and a plain error trace E past the error cutoff, more than a window of
// error traces K kept until the error cutoff, plain traces D past the
// interesting cutoff, and one recent plain trace R. It returns the
// interesting and error cutoffs.
func seedTraceSummaryWindows(t *testing.T, repo *Repository, kept, deletable int) (time.Time, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	insertTraceSummaries(t, repo, "X", 1, now.Add(-300*time.Hour), true)
	insertTraceSummaries(t, repo, "E", 1, now.Add(-300*time.Hour), true)
	insertTraceSummaries(t, repo, "K", kept, now.Add(-100*time.Hour), true)
	insertTraceSummaries(t, repo, "D", deletable, now.Add(-60*time.Hour), false)
	insertTraceSummaries(t, repo, "R", 1, now, false)
	if err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		_, err := db.Exec(`INSERT INTO error_exemplars (project_id, service, name, day, trace_id)
			VALUES (1, 'web', 'root', '2026-01-01', 'X0')`)
		return err
	}); err != nil {
		t.Fatalf("insert exemplar: %v", err)
	}
	return now.Add(-50 * time.Hour), now.Add(-200 * time.Hour)
}

// A window holds only kept summaries when more than a window of error traces
// sit before the plain traces past the interesting cutoff. The pass must walk
// past them and delete the rest, on the writer alone and with the window read
// on the read pool.
func TestDeleteTraceSummariesWalksPastKeptWindows(t *testing.T) {
	const kept, deletable = retentionDeleteBatch + 5, 10
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
			ic, ec := seedTraceSummaryWindows(t, repo, kept, deletable)

			n, err := repo.DeleteTraceSummariesOlderThan(context.Background(), ic, ec)
			if err != nil {
				t.Fatalf("delete summaries: %v", err)
			}
			if n != deletable+1 {
				t.Errorf("deleted %d summaries, want %d", n, deletable+1)
			}
			got := summaryPrefixCounts(t, repo)
			if len(got) != 3 || got["X"] != 1 || got["K"] != kept || got["R"] != 1 {
				t.Errorf("summaries left per prefix = %v, want X:1 K:%d R:1", got, kept)
			}
		})
	}
}

// The DELETE checks each listed row again: a trace that gained an error or an
// exemplar after the window was read keeps its summary, and a listed row id
// that now holds a recent summary stays.
func TestDeleteTraceSummaryRowsChecksEachRowAgain(t *testing.T) {
	repo := setupTestDB(t)
	ic, ec := seedTraceSummaryWindows(t, repo, 0, 2)
	var ids []int64
	if err := repo.execLow(FamilySpans, func(db *sql.DB) error {
		rows, err := db.Query(`SELECT rowid FROM trace_summaries WHERE trace_id IN ('D0', 'D1', 'E0', 'R0')`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE trace_summaries SET has_error = 1 WHERE trace_id = 'D1'`); err != nil {
			return err
		}
		_, err = db.Exec(`INSERT INTO error_exemplars (project_id, service, name, day, trace_id)
			VALUES (1, 'web', 'root', '2026-01-02', 'E0')`)
		return err
	}); err != nil {
		t.Fatalf("read rows: %v", err)
	}

	n, err := deleteRowsByID(context.Background(), repo.writer(FamilySpans), deleteTraceSummaryRowsSQL, ids, 1, ic, ec)
	if err != nil {
		t.Fatalf("delete rows: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d rows, want 1", n)
	}
	if got := summaryPrefixCounts(t, repo); len(got) != 4 || got["D"] != 1 || got["E"] != 1 || got["R"] != 1 {
		t.Errorf("summaries left per prefix = %v, want D:1 E:1 R:1 X:1", got)
	}
}
