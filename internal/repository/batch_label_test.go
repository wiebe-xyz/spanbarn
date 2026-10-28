package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"

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

// labelScheduler is the scheduler whose in-flight label the SQL function
// write_label returns, for tests that read a statement's label from inside it.
var labelScheduler atomic.Pointer[writescheduler.Scheduler]

func init() {
	if err := sqlite.RegisterScalarFunction("write_label", 0,
		func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			if s := labelScheduler.Load(); s != nil {
				return s.InFlight(), nil
			}
			return "", nil
		}); err != nil {
		panic(err)
	}
}

// execLowAffecting names its caller and the family too. Before, every write it
// ran was labelled "execLowAffecting", so an 11s span delete on prod
// (2026-10-28) could not be told apart from the slo_counts or session deletes.
func TestExecLowAffectingLabelNamesMethodAndFamily(t *testing.T) {
	repo := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)
	labelScheduler.Store(s)
	t.Cleanup(func() { labelScheduler.Store(nil) })

	if _, err := repo.DB().Exec(`CREATE TABLE label_probe (label TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.execLowAffecting(FamilyCore, `INSERT INTO label_probe VALUES (write_label())`); err != nil {
		t.Fatal(err)
	}
	want := "TestExecLowAffectingLabelNamesMethodAndFamily:" + FamilyCore.String()
	var got string
	if err := repo.DB().QueryRow(`SELECT label FROM label_probe`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("execLowAffecting label %q, want %q", got, want)
	}
}
