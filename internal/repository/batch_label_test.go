package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// The label of a batched delete names the method that called batchedDelete and
// the family, so a wedge dump or slow-write warning says which retention delete
// is running. The test calls the batchers straight from its own function, so the
// expected method name is this test's.
func TestBatchedDeleteLabelNamesMethodAndFamily(t *testing.T) {
	repo := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)

	want := "TestBatchedDeleteLabelNamesMethodAndFamily:" + FamilyLogs.String()

	var seen string
	if _, err := repo.batchedDelete(ctx, FamilyLogs, func(*sql.DB) (int64, error) {
		seen = s.InFlight()
		return 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != want {
		t.Errorf("batchedDelete label %q, want %q", seen, want)
	}

	seen = ""
	if _, _, err := repo.batchedDeleteLimited(ctx, FamilyLogs, 10, func(*sql.DB, int64) (int64, error) {
		seen = s.InFlight()
		return 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != want {
		t.Errorf("batchedDeleteLimited label %q, want %q", seen, want)
	}
}
