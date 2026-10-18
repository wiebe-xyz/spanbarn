package repository

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// A read that a background worker holds open must leave the single writer
// connection free. On 2026-10-18 at 00:01Z the rollup compactor's window read
// ran on that connection, outside the write scheduler, and TouchAPIKey waited
// 30s for it until the watchdog reported a wedge (#360). The test holds the
// compactor's own window query open through a WithReads copy and requires
// TouchAPIKey to finish while it is open.
func TestWithReadsLeavesTheWriterFreeWhileAReadIsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store := openTestStorage(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	repo := store.Repository()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)

	ro, err := OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatalf("open read db: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	bg := repo.WithReads(ro.DB)

	project, err := repo.CreateProject("p", "P")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	keyID, err := repo.CreateAPIKey(project.ID, "k", "hash", "ingest")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	// Writes through the copy still reach the writer: on the read-only pool
	// this upsert would fail.
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	first, second := coarseRow(3600, day, 1), coarseRow(3600, day.Add(time.Hour), 2)
	first.ProjectID, second.ProjectID = project.ID, project.ID
	if err := bg.UpsertCoarseRollups(ctx, []MetricRollup{first, second}); err != nil {
		t.Fatalf("UpsertCoarseRollups through the copy: %v", err)
	}
	got, err := bg.RollupWindow(ctx, 3600, day, day.Add(24*time.Hour), 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("RollupWindow through the copy = %d rows, %v; want the 2 rows just written", len(got), err)
	}

	// Two rows, so the statement stays open, holding its connection, after
	// the first one.
	rows, err := bg.db.QueryContext(ctx, rollupWindowCoarseQuery, project.ID, 3600, day, day.Add(24*time.Hour), 10)
	if err != nil {
		t.Fatalf("open the window read: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("window read returned no row: %v", rows.Err())
	}

	done := make(chan error, 1)
	go func() { done <- repo.TouchAPIKey(keyID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("TouchAPIKey: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TouchAPIKey is still waiting while the window read is open: the read holds the writer connection")
	}
}
