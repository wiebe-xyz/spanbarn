// Package sampling provides small, reusable sampling primitives shared between
// the ingest and worker layers without creating an import cycle between them.
package sampling

import (
	"strconv"
	"sync"
	"time"
)

const (
	// floorGCInterval is how often expired minute buckets are swept.
	floorGCInterval = 2 * time.Minute

	// floorRetainMinutes is how many minutes of buckets are kept before GC.
	// Generous enough to tolerate batching/replay delays at the writer.
	floorRetainMinutes = 15
)

// MinuteFloor guarantees that at least a minimum number of boring traces survive
// sampling per (project, operation) within each wall-clock minute. It is a
// concurrency-safe in-memory counter; counts reset on process restart, so the
// floor is best-effort rather than durable.
//
// It is intended to back a single-writer chokepoint (the SQLite writer), where
// an in-memory count is accurate. With multiple independent writers the floor
// would multiply by the number of writers.
type MinuteFloor struct {
	mu      sync.Mutex
	buckets map[string]*floorBucket
}

type floorBucket struct {
	count  int
	minute int64 // minutes since epoch, for GC
}

// NewMinuteFloor creates a floor tracker and starts its background GC loop.
func NewMinuteFloor() *MinuteFloor {
	f := &MinuteFloor{buckets: make(map[string]*floorBucket)}
	go f.gcLoop()
	return f
}

// ShouldKeep records a keep decision for one boring trace and reports whether it
// should be stored.
//
//	ratioKeep == true  → the normal ratio sampler already kept it; count it, keep.
//	ratioKeep == false → keep (and count) only if fewer than min traces have been
//	                     kept for this (project, op, minute) bucket — the floor rescue.
//
// minuteBucket is the wall-clock minute of the trace's root span
// (start_time_us / 60_000_000). Counting ratio-keeps toward the bucket avoids
// storing an extra floor trace when the ratio sampler already met the minimum.
func (f *MinuteFloor) ShouldKeep(projectID int64, op string, minuteBucket int64, min int, ratioKeep bool) bool {
	key := bucketKey(projectID, op, minuteBucket)

	f.mu.Lock()
	defer f.mu.Unlock()

	b := f.buckets[key]
	if b == nil {
		b = &floorBucket{minute: minuteBucket}
		f.buckets[key] = b
	}

	if ratioKeep {
		b.count++
		return true
	}
	if min > 0 && b.count < min {
		b.count++
		return true
	}
	return false
}

func bucketKey(projectID int64, op string, minuteBucket int64) string {
	// Compact, allocation-light key. \x00 separators avoid collisions between
	// e.g. op="a\x001" and project boundaries.
	return strconv.FormatInt(projectID, 10) + "\x00" + op + "\x00" + strconv.FormatInt(minuteBucket, 10)
}

func (f *MinuteFloor) gcLoop() {
	ticker := time.NewTicker(floorGCInterval)
	defer ticker.Stop()
	for t := range ticker.C {
		f.gc(t)
	}
}

// gc removes buckets older than floorRetainMinutes relative to now.
// Exposed (unexported but directly callable) so tests can drive it deterministically.
func (f *MinuteFloor) gc(now time.Time) {
	cutoff := now.Unix()/60 - floorRetainMinutes
	f.mu.Lock()
	for key, b := range f.buckets {
		if b.minute < cutoff {
			delete(f.buckets, key)
		}
	}
	f.mu.Unlock()
}
