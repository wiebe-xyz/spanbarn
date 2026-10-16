package repository

import (
	"strings"
	"testing"
	"time"
)

func versionSpan(projectID int64, id, service, status, attrs string, durationUs int64) Span {
	s := makeSpan(projectID, "t"+id, "s"+id, service, "GET /api", status, durationUs)
	s.Attributes = attrs
	return s
}

func TestQueryVersionStatsFromSpans(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	spans := []Span{
		versionSpan(p.ID, "1", "web", "ok", `{"service.version":"1.0.0"}`, 100),
		versionSpan(p.ID, "2", "web", "error", `{"service.version":"1.0.0"}`, 300),
		versionSpan(p.ID, "3", "web", "ok", `{"service.version":"1.1.0"}`, 50),
		versionSpan(p.ID, "4", "web", "ok", `{"key":"no version"}`, 70),
		versionSpan(p.ID, "5", "api", "ok", `{"service.version":"9.9.9"}`, 70),
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	got, err := repo.QueryVersionStatsFromSpans(p.ID, "web", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("QueryVersionStatsFromSpans: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 versions (no unversioned span, no other service), got %d: %+v", len(got), got)
	}
	byVersion := map[string]VersionStats{}
	for _, v := range got {
		byVersion[v.Version] = v
	}
	if v := byVersion["1.0.0"]; v.Count != 2 || v.ErrorCount != 1 || v.P95Us != 300 {
		t.Errorf("1.0.0 = %+v, want count 2, errors 1, p95 300", v)
	}
	if v := byVersion["1.1.0"]; v.Count != 1 || v.ErrorCount != 0 {
		t.Errorf("1.1.0 = %+v, want count 1, errors 0", v)
	}
}

func TestQueryVersionStatsFromSpansNoVersions(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	if err := repo.InsertSpans([]Span{versionSpan(p.ID, "1", "web", "ok", `{}`, 10)}); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}
	got, err := repo.QueryVersionStatsFromSpans(p.ID, "web", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("QueryVersionStatsFromSpans: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a service without service.version must list nothing, got %+v", got)
	}
}

// The version query must walk the project/time index, never the whole spans
// table.
func TestVersionStatsQueryUsesIndex(t *testing.T) {
	repo := setupTestDB(t)
	q, args := versionStatsQuery(1, "web", time.Now().Add(-time.Hour), time.Now())
	rows, err := repo.DB().Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, " | ")
	if !strings.Contains(joined, "USING INDEX idx_spans_project_ingested") && !strings.Contains(joined, "USING COVERING INDEX idx_spans_project_ingested") {
		t.Fatalf("plan does not use idx_spans_project_ingested: %s", joined)
	}
}
