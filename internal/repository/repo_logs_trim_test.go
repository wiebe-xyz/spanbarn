package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// Protected rows that sort before the deletable ones, more of them than one
// batch, must not stop the trim from reaching the rows behind them, and they
// must survive it.
func TestDeleteLogsOlderThanPassesProtectedRowsAcrossBatches(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := repo.PinTrace(ctx, 1, "pinned", "keep"); err != nil {
		t.Fatalf("PinTrace: %v", err)
	}
	protected := retentionDeleteBatch + 500
	deletable := 2*retentionDeleteBatch + 300

	insert := func(trace string, n int) {
		recs := make([]model.LogRecord, n)
		for i := range recs {
			recs[i] = makeLogRecord(1, trace, "row", 9)
		}
		if err := repo.InsertLogs(ctx, recs); err != nil {
			t.Fatalf("InsertLogs: %v", err)
		}
	}
	insert("pinned", protected)
	insert("", deletable)

	n, err := repo.DeleteLogsOlderThan(ctx, now.Add(time.Hour), now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("DeleteLogsOlderThan: %v", err)
	}
	if n != int64(deletable) {
		t.Errorf("deleted %d, want %d", n, deletable)
	}

	var left int
	if err := repo.DB().QueryRow(`SELECT COUNT(*) FROM logs WHERE trace_id = 'pinned'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != protected {
		t.Errorf("pinned rows left = %d, want %d", left, protected)
	}
	if err := repo.DB().QueryRow(`SELECT COUNT(*) FROM logs WHERE trace_id IS NULL`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("unprotected rows left = %d, want 0", left)
	}
}

// Each batch must read the (project_id, ingested_at) index from the cursor on,
// in index order, with no sort step. A different plan means a batch scans or
// sorts more than the rows it returns, which holds the single write slot longer.
func TestLogTrimSelectPlanSeeksProjectIndex(t *testing.T) {
	repo := setupTestDB(t)
	for _, table := range []string{"logs", "kept_logs"} {
		for _, resume := range []bool{false, true} {
			args := []any{1, "2030-01-01"}
			if resume {
				args = append(args, "2020-01-01", 1)
			}
			args = append(args, "2020-01-01", retentionDeleteBatch)

			rows, err := repo.DB().Query("EXPLAIN QUERY PLAN "+logTrimSelectSQL(table, resume), args...)
			if err != nil {
				t.Fatal(err)
			}
			var plan string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				plan += detail + "\n"
			}
			rows.Close()

			if !strings.Contains(plan, "idx_"+table+"_project_ingested") && !strings.Contains(plan, "idx_logs_project_ingested") {
				t.Errorf("%s resume=%v: project index not used:\n%s", table, resume, plan)
			}
			if strings.Contains(plan, "TEMP B-TREE") {
				t.Errorf("%s resume=%v: batch sorts:\n%s", table, resume, plan)
			}
		}
	}
}
