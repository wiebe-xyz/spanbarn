package repository

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// insertAgedPromptRecords inserts n prompt records for a project and backdates
// their ingested_at, which InsertPromptRecords leaves to DEFAULT CURRENT_TIMESTAMP.
func insertAgedPromptRecords(t *testing.T, repo *Repository, projectID int64, n int, age time.Duration) {
	t.Helper()
	records := make([]PromptRecord, n)
	for i := range records {
		records[i] = PromptRecord{
			ProjectID:    projectID,
			TraceID:      fmt.Sprintf("p%d-age%s-%d", projectID, age, i),
			SpanID:       fmt.Sprintf("s%d-age%s-%d", projectID, age, i),
			Service:      "api",
			Name:         "chat",
			PromptBody:   "prompt",
			ResponseBody: "response",
			StartTimeUs:  time.Now().UnixMicro(),
		}
	}
	if err := repo.InsertPromptRecords(records); err != nil {
		t.Fatalf("InsertPromptRecords: %v", err)
	}
	if age == 0 {
		return
	}
	// Only the rows just inserted still carry the current timestamp.
	if _, err := repo.DB().Exec(
		"UPDATE prompt_records SET ingested_at = ? WHERE project_id = ? AND ingested_at > ?",
		time.Now().UTC().Add(-age), projectID, time.Now().UTC().Add(-time.Minute),
	); err != nil {
		t.Fatalf("backdate prompt records: %v", err)
	}
}

func countPromptRecords(t *testing.T, repo *Repository, projectID int64) int {
	t.Helper()
	var n int
	if err := repo.DB().QueryRow("SELECT count(*) FROM prompt_records WHERE project_id = ?", projectID).Scan(&n); err != nil {
		t.Fatalf("count prompt records: %v", err)
	}
	return n
}

// TestDeletePromptRecordsOlderThanLimited covers the window prompt records never
// had: before it, DeletePromptRecordsOlderThan existed but nothing called it, and
// the table grew to the largest in production. The cutoff must spare recent rows
// in every project, and the ceiling must leave a backlog for the next cycle
// rather than draining weeks of rows in one call.
func TestDeletePromptRecordsOlderThanLimited(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	a, _ := repo.CreateProject("a", "A")
	b, _ := repo.CreateProject("b", "B")

	insertAgedPromptRecords(t, repo, a.ID, 6, 40*24*time.Hour)
	insertAgedPromptRecords(t, repo, b.ID, 4, 40*24*time.Hour)
	insertAgedPromptRecords(t, repo, a.ID, 3, 0)
	insertAgedPromptRecords(t, repo, b.ID, 2, 0)

	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)

	n, more, err := repo.DeletePromptRecordsOlderThanLimited(ctx, cutoff, 7)
	if err != nil {
		t.Fatalf("limited delete: %v", err)
	}
	if n != 7 || !more {
		t.Errorf("deleted %d (more=%v), want 7 with a backlog reported", n, more)
	}

	n, more, err = repo.DeletePromptRecordsOlderThanLimited(ctx, cutoff, 100)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n != 3 || more {
		t.Errorf("second pass deleted %d (more=%v), want the remaining 3 and no backlog", n, more)
	}

	if got := countPromptRecords(t, repo, a.ID); got != 3 {
		t.Errorf("project a kept %d records, want its 3 recent ones", got)
	}
	if got := countPromptRecords(t, repo, b.ID); got != 2 {
		t.Errorf("project b kept %d records, want its 2 recent ones", got)
	}
}
