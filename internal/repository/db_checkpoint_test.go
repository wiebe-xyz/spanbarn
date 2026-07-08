package repository

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestCheckpointModes exercises both WAL checkpoint strategies against a
// file-backed DB (WAL mode is a no-op on :memory:). Both must complete without
// error and leave committed data readable. TRUNCATE must additionally reset the
// WAL file to zero bytes; PASSIVE folds frames into the main DB but is allowed to
// leave the WAL file in place (it does not reset the header — that is precisely
// what keeps a co-resident Litestream on the same generation).
func TestCheckpointModes(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	for _, mode := range []CheckpointMode{CheckpointPassive, CheckpointTruncate} {
		t.Run(string(mode), func(t *testing.T) {
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

			db.FinalCheckpoint(mode, discard)

			// Committed data must survive the checkpoint.
			got, err := repo.GetProjectBySlug("p19")
			if err != nil {
				t.Fatalf("GetProjectBySlug after %s checkpoint: %v", mode, err)
			}
			if got.Slug != "p19" {
				t.Fatalf("unexpected project after checkpoint: %+v", got)
			}

			if mode == CheckpointTruncate {
				fi, err := os.Stat(walPath)
				if err != nil {
					t.Fatalf("stat WAL after TRUNCATE: %v", err)
				}
				if fi.Size() != 0 {
					t.Fatalf("TRUNCATE should reset the WAL to 0 bytes, got %d", fi.Size())
				}
			}
		})
	}
}
