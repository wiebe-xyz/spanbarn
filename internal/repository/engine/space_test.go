package engine

import "testing"

// TestSpaceAccountingTreatsFreelistAsAvailable pins the distinction that the
// disk-headroom guard depends on. Freelist pages are used bytes on the
// filesystem but writable space to SQLite. Counting them as used would make a
// healthy database with a large freelist look permanently critical, and the
// guard would shorten retention forever.
func TestSpaceAccountingTreatsFreelistAsAvailable(t *testing.T) {
	const pageSize = 4096
	s := Space{
		PageSize:        pageSize,
		PageCount:       1000,
		FreelistCount:   400,
		VolumeBytes:     10 * 1000 * pageSize,
		VolumeFreeBytes: 0, // volume is 100% full...
	}

	if got, want := s.LiveBytes(), int64(600*pageSize); got != want {
		t.Errorf("LiveBytes = %d, want %d", got, want)
	}
	if got, want := s.ReusableBytes(), int64(400*pageSize); got != want {
		t.Errorf("ReusableBytes = %d, want %d", got, want)
	}
	// ...but 400 reusable pages means there IS room to write.
	if got, want := s.AvailableBytes(), int64(400*pageSize); got != want {
		t.Errorf("AvailableBytes = %d, want %d", got, want)
	}
	if got := s.UsedFraction(); got != 0.96 {
		t.Errorf("UsedFraction = %v, want 0.96 (not 1.0 — freelist is writable)", got)
	}
}

func TestSpaceUsedFractionEdgeCases(t *testing.T) {
	// An unmeasured volume must read as unpressured, never as critical: a
	// failed statfs must not cause retention to start deleting aggressively.
	unmeasured := Space{PageSize: 4096, PageCount: 10}
	if got := unmeasured.UsedFraction(); got != 0 {
		t.Errorf("unmeasured UsedFraction = %v, want 0", got)
	}
	if unmeasured.Measured() {
		t.Error("Measured() = true for a Space with no volume figures")
	}

	// Clamped to [0,1] even if statfs reports more available than total.
	over := Space{PageSize: 4096, PageCount: 10, VolumeBytes: 100, VolumeFreeBytes: 500}
	if got := over.UsedFraction(); got != 0 {
		t.Errorf("over-available UsedFraction = %v, want clamp to 0", got)
	}

	full := Space{PageSize: 4096, PageCount: 10, VolumeBytes: 100, VolumeFreeBytes: 0}
	if got := full.UsedFraction(); got != 1 {
		t.Errorf("full UsedFraction = %v, want 1", got)
	}
	if !full.Measured() {
		t.Error("Measured() = false for a Space with volume figures")
	}
}
