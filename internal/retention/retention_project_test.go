package retention

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// A per-project max-hours cap evicts that project's old non-error traces while
// keeping errors, and leaves other projects (no cap) untouched.
func TestRunOncePerProjectHoursCap(t *testing.T) {
	cfg := Config{
		InterestingRetentionHours: 100000, // keep the main aggregate-then-delete loop off test data
		ErrorRetentionDays:        3650,
		AggregateRetentionDays:    3650,
		SlowThresholdUS:           1_000_000,
	}
	worker, repo := setupTestWorker(t, cfg)
	ctx := context.Background()
	if _, err := repo.CreateProject("p1", "P1"); err != nil { // id 1 (capped)
		t.Fatalf("CreateProject p1: %v", err)
	}
	if _, err := repo.CreateProject("p2", "P2"); err != nil { // id 2 (no cap)
		t.Fatalf("CreateProject p2: %v", err)
	}

	old := time.Now().UTC().Add(-10 * time.Hour)
	mk := func(projectID int64, trace, status string) {
		s := repository.Span{
			ProjectID: projectID, TraceID: trace, SpanID: trace + "s", Name: "op", Service: "svc",
			Kind: "server", Status: status, StartTimeUs: 1000, DurationUs: 100,
			Attributes: "{}", Events: "[]", IngestedAt: old,
		}
		if err := repo.InsertSpans([]repository.Span{s}); err != nil {
			t.Fatalf("insert %s: %v", trace, err)
		}
	}
	mk(1, "p1-ok1", "ok")
	mk(1, "p1-ok2", "ok")
	mk(1, "p1-err", "error")
	mk(2, "p2-ok", "ok") // different project, no cap → must survive

	if err := repo.SetSetting("retention.max_hours.project.1", "5"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	survives := func(projectID int64, trace string) bool {
		rows, err := repo.SearchTraceSummaries(repository.SpanFilter{ProjectID: projectID}, 0)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		for _, r := range rows {
			if r.TraceID == trace {
				return true
			}
		}
		return false
	}

	if survives(1, "p1-ok1") || survives(1, "p1-ok2") {
		t.Error("project 1 old non-error traces should be evicted by the 5h cap")
	}
	if !survives(1, "p1-err") {
		t.Error("project 1 error trace must survive the cap")
	}
	if !survives(2, "p2-ok") {
		t.Error("project 2 (no cap) trace must not be touched")
	}
}
