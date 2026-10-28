package repository

import (
	"strings"
	"testing"
)

// queryPlan returns the EXPLAIN QUERY PLAN details of query, one per line.
func queryPlan(t *testing.T, repo *Repository, query string, args ...any) string {
	t.Helper()
	rows, err := repo.writer(FamilySpans).Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}

// Every chunk of the eviction seeks its table by trace id, and the victim
// read walks the summaries list index; none scans a whole table.
func TestEvictPlansSeekIndexes(t *testing.T) {
	repo := setupTestDB(t)
	args := []any{`["a"]`, 1, "2030-01-01", evictChunk}
	seeks := map[string]string{
		"spans":           "SEARCH x USING INDEX idx_spans_trace (trace_id=?)",
		"error_samples":   "SEARCH x USING INDEX idx_error_samples_trace (trace_id=?)",
		"trace_summaries": "SEARCH t USING INDEX sqlite_autoindex_trace_summaries_1 (project_id=? AND trace_id=?)",
	}
	for _, table := range evictCascadeTables {
		plan := queryPlan(t, repo, evictRowsSQL(table), args...)
		if !strings.Contains(plan, seeks[table]) || strings.Contains(plan, "SCAN x") || strings.Contains(plan, "SCAN t") {
			t.Errorf("%s chunk delete does not seek by trace id:\n%s", table, plan)
		}
	}
	plan := queryPlan(t, repo, evictVictimsSQL, retentionDeleteBatch, 1, "2030-01-01")
	if !strings.Contains(plan, "idx_trace_summaries_list") || strings.Contains(plan, "SCAN t") {
		t.Errorf("victim read does not walk the list index:\n%s", plan)
	}
}
