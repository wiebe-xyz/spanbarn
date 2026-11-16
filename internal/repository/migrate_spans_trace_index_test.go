package repository

import (
	"strings"
	"testing"
)

// traceIndexSeek is how a plan reads idx_spans_trace once it covers the
// trace's structure columns.
const traceIndexSeek = "COVERING INDEX idx_spans_trace (trace_id=? AND project_id=?)"

// Every query that pins idx_spans_trace reads it as a covering index after
// the rebuild, so none fetches span rows from the table for the trace lookup.
func TestTraceIndexPinsReadCoveringIndex(t *testing.T) {
	repo := setupTestDB(t)
	queries := map[string]struct {
		sql  string
		args []any
	}{
		"refresh":     {refreshTraceStructureSQL, []any{1, "x"}},
		"spanless":    {deleteSpanlessSummarySQL, []any{1, "x"}},
		"boring warm": {boringSummaryWarmSQL, []any{1, "x"}},
		"evict chunk": {evictRowsSQL("spans"), []any{`["a"]`, 1, "2030-01-01", evictChunk}},
	}
	for name, q := range queries {
		plan := queryPlan(t, repo, q.sql, q.args...)
		if !strings.Contains(plan, traceIndexSeek) || strings.Contains(plan, "SCAN spans") {
			t.Errorf("%s does not read idx_spans_trace as a covering index:\n%s", name, plan)
		}
	}
}
