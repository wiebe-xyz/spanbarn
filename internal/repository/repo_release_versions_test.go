package repository

import (
	"strings"
	"testing"
	"time"
)

func TestRecentSpanVersions(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	if err := repo.InsertSpans([]Span{
		versionSpan(p.ID, "1", "web", "ok", `{"service.version":"1.0.0"}`, 10),
		versionSpan(p.ID, "2", "web", "ok", `{"service.version":"1.0.0"}`, 10),
		versionSpan(p.ID, "3", "api", "ok", `{"service.version":"2.0.0"}`, 10),
		versionSpan(p.ID, "4", "web", "ok", `{}`, 10),
	}); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	got, maxID, err := repo.RecentSpanVersions(0, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatalf("RecentSpanVersions: %v", err)
	}
	versions := map[string]bool{}
	for _, s := range got {
		versions[s.Version] = true
		if s.ProjectID != p.ID || s.FirstSeen.IsZero() {
			t.Errorf("sighting %+v: want project %d and a first-seen time", s, p.ID)
		}
	}
	if len(got) != 2 || !versions["1.0.0"] || !versions["2.0.0"] {
		t.Fatalf("want versions 1.0.0 and 2.0.0 once each, got %+v", got)
	}
	if maxID == 0 {
		t.Fatalf("want the newest span id as the next watermark, got 0")
	}

	none, _, err := repo.RecentSpanVersions(0, time.Now().UTC().Add(time.Hour))
	if err != nil || len(none) != 0 {
		t.Fatalf("a window after every span must list nothing, got %+v, %v", none, err)
	}
}

// After the first scan, each scan reads only the spans stored since the
// previous one, by span id. A trace flushed late out of staging keeps its old
// ingested_at, so an ingested_at window would miss it; the id range does not.
func TestRecentSpanVersionsReadsOnlyNewSpans(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	if err := repo.InsertSpans([]Span{versionSpan(p.ID, "1", "web", "ok", `{"service.version":"1.0.0"}`, 10)}); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}
	_, watermark, err := repo.RecentSpanVersions(0, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}

	again, same, err := repo.RecentSpanVersions(watermark, time.Time{})
	if err != nil || len(again) != 0 || same != watermark {
		t.Fatalf("no new spans: want nothing and watermark %d, got %+v, %d, %v", watermark, again, same, err)
	}

	if err := repo.InsertSpans([]Span{
		versionSpan(p.ID, "2", "web", "ok", `{"service.version":"1.0.0"}`, 10),
		versionSpan(p.ID, "3", "web", "ok", `{"service.version":"1.1.0"}`, 10),
	}); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}
	if _, err := repo.DB().Exec(`UPDATE spans SET ingested_at = ? WHERE span_id = 's3'`, time.Now().UTC().Add(-48*time.Hour)); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	got, next, err := repo.RecentSpanVersions(watermark, time.Time{})
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if next <= watermark {
		t.Fatalf("watermark must move past %d, got %d", watermark, next)
	}
	versions := map[string]bool{}
	for _, s := range got {
		versions[s.Version] = true
	}
	if len(got) != 2 || !versions["1.0.0"] || !versions["1.1.0"] {
		t.Fatalf("want the two new spans' versions, including the backdated one, got %+v", got)
	}
}

func TestReleaseExists(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	if ok, err := repo.ReleaseExists(p.ID, "1.0.0"); err != nil || ok {
		t.Fatalf("before create: ok=%v err=%v, want false", ok, err)
	}
	if _, err := repo.CreateRelease(p.ID, "1.0.0", time.Now()); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if ok, err := repo.ReleaseExists(p.ID, "1.0.0"); err != nil || !ok {
		t.Fatalf("after create: ok=%v err=%v, want true", ok, err)
	}
}

func TestAsTime(t *testing.T) {
	want := time.Date(2026, 10, 16, 4, 5, 6, 0, time.UTC)
	if got := asTime(want); !got.Equal(want) {
		t.Errorf("time.Time: got %v", got)
	}
	if got := asTime("2026-10-16 04:05:06"); !got.Equal(want) {
		t.Errorf("plain datetime string: got %v", got)
	}
	if got := asTime("2026-10-16T04:05:06Z"); !got.Equal(want) {
		t.Errorf("RFC3339 string: got %v", got)
	}
	if got := asTime(nil); got.IsZero() {
		t.Errorf("unreadable value must fall back to a real time, got zero")
	}
}

// The recent-version scan must use the ingested_at index, not walk the table.
func TestRecentSpanVersionsUsesIndex(t *testing.T) {
	repo := setupTestDB(t)
	rows, err := repo.DB().Query(`EXPLAIN QUERY PLAN SELECT project_id, json_extract(attributes, '$."service.version"') AS version, MIN(ingested_at)
		FROM spans WHERE ingested_at >= ? AND kind = 'server' AND json_extract(attributes, '$."service.version"') IS NOT NULL
		GROUP BY project_id, version`, time.Now())
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
	if joined := strings.Join(plan, " | "); !strings.Contains(joined, "idx_spans_ingested") && !strings.Contains(joined, "idx_spans_project_ingested") {
		t.Fatalf("plan does not use an ingested_at index: %s", joined)
	}
}

// Scans after the first walk only the new span ids, as a rowid range.
func TestRecentSpanVersionsWatermarkUsesRowidRange(t *testing.T) {
	repo := setupTestDB(t)
	rows, err := repo.DB().Query(`EXPLAIN QUERY PLAN SELECT project_id, json_extract(attributes, '$."service.version"') AS version, MIN(ingested_at)
		FROM spans WHERE id > ? AND id <= ? AND kind = 'server' AND json_extract(attributes, '$."service.version"') IS NOT NULL
		GROUP BY project_id, version`, 1, 2)
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
	if joined := strings.Join(plan, " | "); !strings.Contains(joined, "INTEGER PRIMARY KEY (rowid>? AND rowid<?)") {
		t.Fatalf("plan does not walk a rowid range: %s", joined)
	}
}
