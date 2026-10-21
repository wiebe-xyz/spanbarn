package repository

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// The log trim reads its window before it needs the writer, and the DELETE
// checks each row again. On 2026-10-19 and 2026-10-20 the trim's window read
// ran inside its write slot on the writer connection, and four times one
// window held the slot past the watchdog's 30s (#360).
//
// The test holds the writer's only connection. A trim that reads through the
// read-only pool scans its window while the writer is held and then waits for
// the writer to delete. While it waits, the held connection pins one scanned
// row's trace and adds a row the window cannot have seen. Then the writer is
// released. The pinned row must survive because the DELETE checks it again,
// and the new row must survive because the window was read before it existed.
// A trim that reads on the writer only starts reading after the release, so
// it sees the new row and deletes it.
func TestLogTrimReadsTheWindowBeforeItTakesTheWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store := openTestStorage(t, path)
	ctx := context.Background()
	repo := store.Repository()

	ro, err := OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatalf("open read db: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	repo.SetLogTrimReads(ro.DB)

	pinned, deletable := makeLogRecord(1, "pin-later", "pinned", 9), makeLogRecord(1, "plain", "deletable", 9)
	if err := repo.InsertLogs(ctx, []model.LogRecord{pinned, deletable}); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}
	now := time.Now().UTC()
	cutoff, errCutoff := now.Add(time.Hour), now.Add(-30*24*time.Hour)

	writer := repo.writer(TableFamily("logs"))
	conn, err := writer.Conn(ctx)
	if err != nil {
		t.Fatalf("hold the writer: %v", err)
	}
	waits := writer.Stats().WaitCount

	type result struct {
		n   int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := repo.trimLogsFrom(ctx, "logs", 1, cutoff, errCutoff)
		done <- result{n, err}
	}()

	// The trim asks for the writer once it has its window.
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for writer.Stats().WaitCount == waits {
		select {
		case r := <-done:
			t.Fatalf("trim returned (%d, %v) while the writer was held", r.n, r.err)
		case <-deadline:
			t.Fatal("trim never asked for the writer")
		case <-tick.C:
		}
	}

	if _, err := conn.ExecContext(ctx,
		`INSERT INTO pinned_traces (project_id, trace_id, label) VALUES (1, 'pin-later', 'keep')`); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO logs (project_id, trace_id, time_unix_nano, body, ingested_at)
		SELECT project_id, NULL, time_unix_nano, 'unseen', ingested_at FROM logs WHERE body = 'deletable'`); err != nil {
		t.Fatalf("add a row: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("release the writer: %v", err)
	}

	var r result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("trim still running 5s after the writer was released")
	}
	if r.err != nil {
		t.Fatalf("trimLogsFrom: %v", r.err)
	}
	if r.n != 1 {
		t.Errorf("deleted %d rows, want 1 (the unpinned row the window saw)", r.n)
	}
	if got := logBodies(t, repo); len(got) != 2 || got[0] != "pinned" || got[1] != "unseen" {
		t.Fatalf("rows left = %v, want [pinned unseen]: the DELETE must check the pin again, and the window must be read before the writer is free", got)
	}

	// The next pass resumes after the cursor and reaches the new row.
	n, err := repo.trimLogsFrom(ctx, "logs", 1, cutoff, errCutoff)
	if err != nil || n != 1 {
		t.Fatalf("next pass deleted %d rows (%v), want the 1 row the first window did not see", n, err)
	}
}

func logBodies(t *testing.T, repo *Repository) []string {
	t.Helper()
	rows, err := repo.DB().Query(`SELECT body FROM logs ORDER BY rowid`)
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}
