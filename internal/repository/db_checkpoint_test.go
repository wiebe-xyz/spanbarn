package repository

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
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
	if err := Migrate(db.DB); err != nil {
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
	if err := Migrate(db.DB); err != nil {
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
