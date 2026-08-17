package repository

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBallastReservesRealBlocks is the test that matters. A sparse file
// reserves nothing, so releasing it would free nothing — the ballast would read
// as protection while doing exactly nothing, which is this codebase's recurring
// bug shape. Assert the file actually occupies the blocks it claims.
func TestBallastReservesRealBlocks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "spanbarn.db")
	const size = 2 << 20 // 2 MiB

	b := NewBallast(dbPath, size)
	if !b.Enabled() {
		t.Fatal("Enabled() = false for a configured ballast")
	}
	if err := b.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	info, err := os.Stat(b.Path())
	if err != nil {
		t.Fatalf("stat ballast: %v", err)
	}
	if info.Size() != size {
		t.Errorf("ballast size = %d, want %d", info.Size(), size)
	}
	// st_blocks is in 512-byte units. A sparse file reports far fewer blocks
	// than its apparent size; a real one reports at least as many.
	if blocks := diskBlocks(t, b.Path()); blocks*512 < size {
		t.Errorf("ballast occupies %d bytes of blocks for an apparent size of %d — "+
			"it is sparse and reserves nothing", blocks*512, size)
	}
	if !b.Present() {
		t.Error("Present() = false right after Ensure")
	}
}

func TestBallastEnsureIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "spanbarn.db")
	b := NewBallast(dbPath, 1<<20)

	if err := b.Ensure(); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	first, err := os.Stat(b.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := b.Ensure(); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	second, err := os.Stat(b.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if first.Size() != second.Size() {
		t.Errorf("size changed across Ensure calls: %d -> %d", first.Size(), second.Size())
	}
}

func TestBallastRelease(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "spanbarn.db")
	const size = 1 << 20
	b := NewBallast(dbPath, size)

	if err := b.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	freed, err := b.Release()
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if freed != size {
		t.Errorf("freed = %d, want %d", freed, size)
	}
	if _, err := os.Stat(b.Path()); !os.IsNotExist(err) {
		t.Error("ballast file still present after Release")
	}
	if b.Present() {
		t.Error("Present() = true after Release")
	}

	// The emergency path calls Release unconditionally, so a second call must
	// be a harmless no-op rather than an error.
	freed, err = b.Release()
	if err != nil {
		t.Fatalf("second Release: %v", err)
	}
	if freed != 0 {
		t.Errorf("second Release freed = %d, want 0", freed)
	}

	// And it must be restorable, so the next emergency has a way out too.
	if err := b.Ensure(); err != nil {
		t.Fatalf("Ensure after Release: %v", err)
	}
	if !b.Present() {
		t.Error("ballast not restored")
	}
}

// TestBallastDisabled covers the "not configured" path: every method must be a
// safe no-op so callers need no special-casing.
func TestBallastDisabled(t *testing.T) {
	for _, b := range []*Ballast{
		NewBallast("", 1<<20),
		NewBallast(":memory:", 1<<20),
		NewBallast(filepath.Join(t.TempDir(), "spanbarn.db"), 0),
		nil,
	} {
		if b.Enabled() {
			t.Errorf("Enabled() = true for %+v", b)
		}
		if err := b.Ensure(); err != nil {
			t.Errorf("Ensure on disabled ballast: %v", err)
		}
		freed, err := b.Release()
		if err != nil || freed != 0 {
			t.Errorf("Release on disabled ballast = (%d, %v), want (0, nil)", freed, err)
		}
		if b.Present() {
			t.Error("Present() = true for a disabled ballast")
		}
	}
}

// TestBallastShortFileIsRestored covers a ballast truncated by something else:
// a short reserve is not a reserve, so Ensure must bring it back to full size.
func TestBallastShortFileIsRestored(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "spanbarn.db")
	const size = 1 << 20
	b := NewBallast(dbPath, size)
	if err := b.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := os.Truncate(b.Path(), size/4); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if b.Present() {
		t.Error("Present() = true for a truncated ballast")
	}
	if err := b.Ensure(); err != nil {
		t.Fatalf("Ensure after truncate: %v", err)
	}
	info, err := os.Stat(b.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != size {
		t.Errorf("size after restore = %d, want %d", info.Size(), size)
	}
}
