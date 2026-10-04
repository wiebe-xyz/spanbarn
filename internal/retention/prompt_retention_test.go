package retention

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// insertPromptRecordsAged inserts n prompt records for project 1, backdated by age.
func insertPromptRecordsAged(t *testing.T, repo *repository.Repository, n int, age time.Duration) {
	t.Helper()
	records := make([]repository.PromptRecord, n)
	for i := range records {
		records[i] = repository.PromptRecord{
			ProjectID:   1,
			TraceID:     fmt.Sprintf("prompt-%s-%d", age, i),
			SpanID:      fmt.Sprintf("prompt-span-%s-%d", age, i),
			Service:     "api",
			Name:        "chat",
			StartTimeUs: time.Now().UnixMicro(),
		}
	}
	if err := repo.InsertPromptRecords(records); err != nil {
		t.Fatalf("InsertPromptRecords: %v", err)
	}
	if age == 0 {
		return
	}
	if _, err := repo.DB().Exec(
		"UPDATE prompt_records SET ingested_at = ? WHERE ingested_at > ?",
		time.Now().UTC().Add(-age), time.Now().UTC().Add(-time.Minute),
	); err != nil {
		t.Fatalf("backdate prompt records: %v", err)
	}
}

func promptRecordCount(t *testing.T, repo *repository.Repository) int {
	t.Helper()
	var n int
	if err := repo.DB().QueryRow("SELECT count(*) FROM prompt_records").Scan(&n); err != nil {
		t.Fatalf("count prompt records: %v", err)
	}
	return n
}

// TestRunOnceDeletesOldPromptRecords: the retention cycle is the only caller of
// the prompt delete. Without this wiring the window is a config value that reads
// back fine while the table grows forever, which is what happened in production.
func TestRunOnceDeletesOldPromptRecords(t *testing.T) {
	worker, repo := setupTestWorker(t, Config{InterestingRetentionHours: 48, PromptRetentionDays: 30})
	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	insertPromptRecordsAged(t, repo, 5, 31*24*time.Hour)
	insertPromptRecordsAged(t, repo, 3, 29*24*time.Hour)
	insertPromptRecordsAged(t, repo, 2, 0)

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := promptRecordCount(t, repo); got != 5 {
		t.Errorf("kept %d prompt records, want the 5 inside the 30-day window", got)
	}
}

// TestPromptRetentionDefaultsToThirtyDays pins the default window.
func TestPromptRetentionDefaultsToThirtyDays(t *testing.T) {
	if got := (Config{}).withDefaults().PromptRetentionDays; got != 30 {
		t.Errorf("default PromptRetentionDays = %d, want 30", got)
	}
}

// TestPromptRetentionSettingOverrides: the window is adjustable at runtime from
// the settings table, like every other retention window.
func TestPromptRetentionSettingOverrides(t *testing.T) {
	worker, repo := setupTestWorker(t, Config{PromptRetentionDays: 30})
	if err := repo.SetSetting("prompt_retention_days", "45"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got := worker.effectiveConfig().PromptRetentionDays; got != 45 {
		t.Errorf("effective PromptRetentionDays = %d, want 45 from the setting", got)
	}
}
