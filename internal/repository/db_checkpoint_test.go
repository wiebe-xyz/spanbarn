package repository

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCheckpoint exercises a TRUNCATE checkpoint against a file-backed DB
// (WAL mode is a no-op on :memory:). It must complete without error, leave
// committed data readable, and reset the WAL file to zero bytes.
func TestCheckpoint(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	path := filepath.Join(t.TempDir(), "cp.db")
	db, err := NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)

	// Generate WAL frames. wal_autocheckpoint(0) means nothing is folded
	// into the main DB until we checkpoint explicitly.
	for i := 0; i < 20; i++ {
		if _, err := repo.CreateProject(fmt.Sprintf("p%d", i), "x"); err != nil {
			t.Fatalf("CreateProject %d: %v", i, err)
		}
	}
	walPath := path + "-wal"
	if fi, err := os.Stat(walPath); err != nil || fi.Size() == 0 {
		t.Fatalf("expected a non-empty WAL before checkpoint (err=%v)", err)
	}

	db.FinalCheckpoint(discard)

	// Committed data must survive the checkpoint.
	got, err := repo.GetProjectBySlug("p19")
	if err != nil {
		t.Fatalf("GetProjectBySlug after checkpoint: %v", err)
	}
	if got.Slug != "p19" {
		t.Fatalf("unexpected project after checkpoint: %+v", got)
	}

	fi, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat WAL after checkpoint: %v", err)
	}
	if fi.Size() != 0 {
		t.Fatalf("TRUNCATE should reset the WAL to 0 bytes, got %d", fi.Size())
	}
}

// TestCheckpointFrameCount verifies checkpoint() reports 0 WAL frames after a
// TRUNCATE reset.
func TestCheckpointFrameCount(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "cp.db")
	db, err := NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)
	for i := 0; i < 50; i++ {
		if _, err := repo.CreateProject(fmt.Sprintf("p%d", i), "x"); err != nil {
			t.Fatalf("CreateProject %d: %v", i, err)
		}
	}
	ctx := context.Background()

	if n := db.checkpoint(ctx, 0, discard); n != 0 {
		t.Fatalf("TRUNCATE checkpoint should report 0 WAL frames after reset, got %d", n)
	}
}

// TestCheckpointDoesNotWaitForReader holds a read snapshot open while the
// writer adds WAL frames, the way a reader pod or the settings snapshot does.
// The checkpoint must report the WAL as busy at once instead of holding the
// single writer connection in SQLite's busy handler, and a write right after it
// must not queue behind it (#337). Once the reader lets go, the next
// checkpoint resets the WAL.
func TestCheckpointDoesNotWaitForReader(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "cp.db")
	db, err := NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := MigrateSingleFile(ctx, db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)
	if _, err := repo.CreateProject("before", "x"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	reader, err := NewReadOnlyDB(path)
	if err != nil {
		t.Fatalf("NewReadOnlyDB: %v", err)
	}
	t.Cleanup(func() { reader.Close() })
	tx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin read tx: %v", err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM projects").Scan(&n); err != nil {
		t.Fatalf("read inside snapshot: %v", err)
	}

	// Frames past the reader's snapshot, which the checkpoint cannot reset.
	for i := 0; i < 5; i++ {
		if _, err := repo.CreateProject(fmt.Sprintf("after%d", i), "x"); err != nil {
			t.Fatalf("CreateProject %d: %v", i, err)
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	frames := db.checkpoint(cctx, 0, discard)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("checkpoint waited %s for the reader; it must return busy at once", took)
	}
	if frames <= 0 {
		t.Fatalf("checkpoint with a reader holding the WAL reported %d frames, want the WAL still in use", frames)
	}

	start = time.Now()
	if _, err := repo.CreateProject("write-after-checkpoint", "x"); err != nil {
		t.Fatalf("write after checkpoint: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("write after a busy checkpoint took %s", took)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("end read tx: %v", err)
	}
	if n := db.checkpoint(ctx, 0, discard); n != 0 {
		t.Fatalf("checkpoint after the reader released reported %d frames, want 0", n)
	}
}
