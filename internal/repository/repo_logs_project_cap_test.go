package repository

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

func insertProjectLogs(t *testing.T, repo *Repository, projectID int64, traceID string, n int) {
	t.Helper()
	logs := make([]model.LogRecord, n)
	for i := range logs {
		logs[i] = makeLogRecord(projectID, traceID, "log", 9)
	}
	if err := repo.InsertLogs(context.Background(), logs); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}
}

// Each job holds one chunk and reports its rows in write.rows.
func TestDeleteProjectLogsRunsInChunksAndReportsRows(t *testing.T) {
	jobRows := recordJobRows(t)
	repo := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)

	insertProjectLogs(t, repo, 1, "", projectLogCapChunk+3)
	insertProjectLogs(t, repo, 2, "", 1)

	n, err := repo.DeleteProjectLogsOlderThan(ctx, 1, time.Now().Add(time.Hour).UTC())
	if err != nil {
		t.Fatalf("DeleteProjectLogsOlderThan: %v", err)
	}
	if n != projectLogCapChunk+3 {
		t.Errorf("deleted %d, want %d", n, projectLogCapChunk+3)
	}
	label := "DeleteProjectLogsOlderThan:" + TableFamily("logs").String()
	if got, want := jobRows()[label], []int64{projectLogCapChunk, 3}; !slices.Equal(got, want) {
		t.Errorf("%s job rows = %v, want %v", label, got, want)
	}
	var left int
	if err := repo.db.QueryRow(`SELECT count(*) FROM logs`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if got := left; got != 1 {
		t.Errorf("%d rows left, want the other project's 1", got)
	}
}

// The chunk is read outside the write slot, so the DELETE checks each row again.
func TestDeleteProjectLogRowsChecksEachRowAgain(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	insertProjectLogs(t, repo, 1, "pinned-since", 1)
	insertProjectLogs(t, repo, 1, "", 1)
	insertProjectLogs(t, repo, 2, "", 1)
	cutoff := time.Now().Add(time.Hour).UTC()

	ids := func() []int64 {
		rows, err := repo.db.Query(`SELECT rowid FROM logs`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}()
	if len(ids) != 3 {
		t.Fatalf("%d rows, want 3", len(ids))
	}
	if err := repo.PinTrace(ctx, 1, "pinned-since", "keep"); err != nil {
		t.Fatalf("PinTrace: %v", err)
	}

	var n int64
	err := repo.submit(TableFamily("logs"), writescheduler.Low, "test", func(db *sql.DB) error {
		var e error
		n, e = deleteRowsByID(ctx, db, deleteProjectLogRowsSQL("logs"), ids, int64(1), cutoff)
		return e
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d of the listed rows, want only the unpinned row of project 1", n)
	}
	early, err := deleteRowsByID(ctx, repo.db, deleteProjectLogRowsSQL("logs"), ids, int64(1), time.Now().Add(-time.Hour).UTC())
	if err != nil || early != 0 {
		t.Errorf("before the cutoff deleted %d (err %v), want 0", early, err)
	}
}
