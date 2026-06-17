package sampling

import (
	"testing"
	"time"
)

// newFloorNoGC builds a MinuteFloor without starting the GC goroutine so tests
// can drive gc() deterministically.
func newFloorNoGC() *MinuteFloor {
	return &MinuteFloor{buckets: make(map[string]*floorBucket)}
}

func TestFloorRescuesExactlyMinWhenRatioDrops(t *testing.T) {
	f := newFloorNoGC()
	const min = 1
	kept := 0
	for i := 0; i < 10; i++ {
		if f.ShouldKeep(1, "GET /health", 100, min, false) {
			kept++
		}
	}
	if kept != min {
		t.Fatalf("expected %d boring trace rescued by floor, got %d", min, kept)
	}
}

func TestFloorRescuesMinGreaterThanOne(t *testing.T) {
	f := newFloorNoGC()
	const min = 3
	kept := 0
	for i := 0; i < 10; i++ {
		if f.ShouldKeep(1, "op", 100, min, false) {
			kept++
		}
	}
	if kept != min {
		t.Fatalf("expected %d rescued, got %d", min, kept)
	}
}

func TestRatioKeepsAlwaysKeptAndCountTowardFloor(t *testing.T) {
	f := newFloorNoGC()
	const min = 1
	// A ratio-keep should always be stored...
	if !f.ShouldKeep(1, "op", 100, min, true) {
		t.Fatal("ratioKeep=true must always keep")
	}
	// ...and it satisfies the floor, so a subsequent ratio-drop is NOT rescued.
	if f.ShouldKeep(1, "op", 100, min, false) {
		t.Fatal("floor already satisfied by ratio-keep; ratio-drop must not be rescued")
	}
}

func TestFloorIndependentPerOpProjectMinute(t *testing.T) {
	f := newFloorNoGC()
	const min = 1
	cases := []struct {
		project int64
		op      string
		minute  int64
	}{
		{1, "a", 100},
		{1, "b", 100}, // different op
		{2, "a", 100}, // different project
		{1, "a", 101}, // different minute
	}
	for _, c := range cases {
		if !f.ShouldKeep(c.project, c.op, c.minute, min, false) {
			t.Fatalf("first boring trace for %+v should be rescued", c)
		}
		// Second in the same bucket is dropped.
		if f.ShouldKeep(c.project, c.op, c.minute, min, false) {
			t.Fatalf("second boring trace for %+v should be dropped", c)
		}
	}
}

func TestFloorMinZeroKeepsNothingExtra(t *testing.T) {
	f := newFloorNoGC()
	if f.ShouldKeep(1, "op", 100, 0, false) {
		t.Fatal("min=0 must not rescue any boring trace")
	}
}

func TestGCEvictsOldBuckets(t *testing.T) {
	f := newFloorNoGC()
	now := time.Unix(1_000*60, 0) // minute index 1000
	nowMinute := now.Unix() / 60

	// Fresh bucket (current minute) and an old bucket (20 minutes ago).
	f.ShouldKeep(1, "fresh", nowMinute, 1, false)
	f.ShouldKeep(1, "old", nowMinute-20, 1, false)

	if len(f.buckets) != 2 {
		t.Fatalf("expected 2 buckets before gc, got %d", len(f.buckets))
	}

	f.gc(now)

	if len(f.buckets) != 1 {
		t.Fatalf("expected 1 bucket after gc, got %d", len(f.buckets))
	}
	if _, ok := f.buckets[bucketKey(1, "fresh", nowMinute)]; !ok {
		t.Fatal("fresh bucket should survive gc")
	}
}
