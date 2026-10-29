package repository

import (
	"strings"
	"testing"
	"time"
)

// The error trace read walks a partial error-summary index in ingested_at
// order: no scan of trace_summaries and no sort, with and without a project.
func TestErrorTracesQueryReadsTheErrorSummaryIndex(t *testing.T) {
	repo := setupTestDB(t)
	to := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		f     SpanFilter
		index string
	}{
		{"all projects", SpanFilter{}, "idx_trace_summaries_error "},
		{"one project", SpanFilter{ProjectID: 5}, "idx_trace_summaries_error_project "},
		{"one project and service", SpanFilter{ProjectID: 5, Service: "api"}, "idx_trace_summaries_error_project "},
	} {
		f := tc.f
		f.Status = "error"
		f.From, f.To = to.Add(-24*time.Hour), to
		q, args, err := errorTracesQuery(f)
		if err != nil {
			t.Fatal(err)
		}
		plan := queryPlan(t, repo, q, args...)
		if !strings.Contains(plan, "USING COVERING INDEX "+tc.index) ||
			strings.Contains(plan, "SCAN") || strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%s: plan does not read %s alone:\n%s", tc.name, strings.TrimSpace(tc.index), plan)
		}
	}
}
