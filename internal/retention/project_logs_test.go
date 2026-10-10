package retention

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func projectLog(projectID int64, body string) model.LogRecord {
	attrs, _ := json.Marshal(map[string]string{"service.name": "svc"})
	ts := uint64(time.Now().UnixNano())
	return model.LogRecord{ProjectID: projectID, TimeUnixNano: ts, ObservedTimeUnixNano: ts, Body: body, Attributes: attrs}
}

// retention.log_hours.project.{id} caps one project's logs and leaves the
// others on the global window.
func TestEvictProjectLogs(t *testing.T) {
	worker, repo := setupTestWorker(t, Config{})
	ctx := context.Background()
	for _, slug := range []string{"capped", "free"} {
		if _, err := repo.CreateProject(slug, slug); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
	}
	if err := repo.InsertLogs(ctx, []model.LogRecord{projectLog(1, "a"), projectLog(2, "b")}); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}
	if err := repo.SetSetting("retention.log_hours.project.1", "168"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	// Eight days on, the 168h cap has passed for the logs just written.
	n, err := worker.evictProjectLogs(ctx, time.Now().UTC().Add(8*24*time.Hour))
	if err != nil {
		t.Fatalf("evictProjectLogs: %v", err)
	}
	if n != 1 {
		t.Errorf("evicted %d, want 1 (project 1 only)", n)
	}
}
