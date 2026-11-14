package repository

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// TestDBSpaceOnRealDatabase exercises the pragma+statfs path against a real
// on-disk SQLite file, which is the only way to confirm the pragmas scan into
// the right fields and that statfs reports a plausible volume.
func TestDBSpaceOnRealDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spanbarn.db")

	db, err := engine.NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)

	space, err := repo.DBSpace(context.Background(), path)
	if err != nil {
		t.Fatalf("DBSpace: %v", err)
	}

	if space.PageSize <= 0 {
		t.Errorf("PageSize = %d, want > 0", space.PageSize)
	}
	if space.PageCount <= 0 {
		t.Errorf("PageCount = %d, want > 0", space.PageCount)
	}
	if space.FileBytes <= 0 {
		t.Errorf("FileBytes = %d, want > 0", space.FileBytes)
	}
	if !space.Measured() {
		t.Fatal("Measured() = false for a real on-disk database")
	}
	if space.VolumeBytes <= 0 || space.VolumeFreeBytes < 0 {
		t.Errorf("implausible volume figures: total=%d free=%d", space.VolumeBytes, space.VolumeFreeBytes)
	}
	if f := space.UsedFraction(); f < 0 || f > 1 {
		t.Errorf("UsedFraction = %v, want within [0,1]", f)
	}
}

// TestDBSpaceInMemoryIsUnmeasured guards the degradation path: an in-memory
// database has no volume, and that must not be an error.
func TestDBSpaceInMemoryIsUnmeasured(t *testing.T) {
	repo := setupTestDB(t)

	space, err := repo.DBSpace(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("DBSpace: %v", err)
	}
	if space.Measured() {
		t.Error("Measured() = true for an in-memory database")
	}
	if space.PageSize <= 0 {
		t.Errorf("PageSize = %d, want the pragmas to still be read", space.PageSize)
	}
}

func TestIsDiskFull(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sqlite full text", errors.New("database or disk is full (13)"), true},
		{"sqlite full uppercase", errors.New("SQLITE_FULL: database or disk is full"), true},
		{"enospc wrapped", fmt.Errorf("write spans: %w", syscall.ENOSPC), true},
		{"no space left", errors.New("write /var/lib/spanbarn: no space left on device"), true},
		{"wrapped sqlite full", fmt.Errorf("insert spans: %w", errors.New("database or disk is full")), true},
		{"busy is not full", errors.New("database is locked (5)"), false},
		{"unrelated", errors.New("constraint failed"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := engine.IsDiskFull(tt.err); got != tt.want {
				t.Errorf("IsDiskFull(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
