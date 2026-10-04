// Package sampling provides small, reusable sampling primitives shared between
// the ingest and worker layers without creating an import cycle between them.
package sampling

import (
	"strconv"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/observability"
)

const (
	// floorGCInterval is how often expired buckets are swept.
	floorGCInterval = 2 * time.Minute

	// floorRetainWindow is the minimum time span of past buckets kept before GC.
	// Generous enough to tolerate batching/replay delays at the writer.
	floorRetainWindow = 15 * time.Minute
)

// MinuteFloor guarantees that at least a minimum number of boring traces survive
// sampling per (project, operation) within each wall-clock bucket (one minute
// for NewMinuteFloor, any width for NewBucketFloor). It is a
// concurrency-safe in-memory counter; counts reset on process restart, so the
// floor is best-effort rather than durable.
//
// It is intended to back a single-writer chokepoint (the SQLite writer), where
// an in-memory count is accurate. With multiple independent writers the floor
// would multiply by the number of writers.
type MinuteFloor struct {
	mu      sync.Mutex
	buckets map[string]*floorBucket
	bucket  time.Duration // width of one bucket; one minute for NewMinuteFloor (zero value also means one minute)
}

type floorBucket struct {
	count int
	index int64 // bucket index (time since epoch / bucket width), for GC
}

// NewMinuteFloor creates a floor tracker and starts its background GC loop.
func NewMinuteFloor() *MinuteFloor {
	f := &MinuteFloor{buckets: make(map[string]*floorBucket), bucket: time.Minute}
	observability.SafeGo("minute-floor-gc", nil, f.gcLoop)
	return f
}

// NewBucketFloor creates a floor tracker whose buckets are bucket wide instead
// of one minute. GC retention scales with the bucket width, see retainBuckets.
func NewBucketFloor(bucket time.Duration) *MinuteFloor {
	f := &MinuteFloor{buckets: make(map[string]*floorBucket), bucket: bucket}
	observability.SafeGo("bucket-floor-gc", nil, f.gcLoop)
	return f
}

// BucketOf returns the bucket index of a trace whose root span starts at
// startTimeUs (microseconds since epoch). Pass it to ShouldKeep.
func (f *MinuteFloor) BucketOf(startTimeUs int64) int64 {
	return startTimeUs / f.width().Microseconds()
}

// width returns the bucket width, defaulting to one minute for a zero value.
func (f *MinuteFloor) width() time.Duration {
	if f.bucket <= 0 {
		return time.Minute
	}
	return f.bucket
}

// retainBuckets is how many buckets before the current one survive GC: enough
// to cover floorRetainWindow, and at least 1 so the previous bucket is always
// kept. That is 15 for one-minute buckets and 1 for hourly buckets.
func (f *MinuteFloor) retainBuckets() int64 {
	w := f.width()
	n := int64((floorRetainWindow + w - 1) / w)
	if n < 1 {
		n = 1
	}
	return n
}

// ShouldKeep records a keep decision for one boring trace and reports whether it
// should be stored. The bucket argument is the index returned by BucketOf.
//
//	ratioKeep == true  → the normal ratio sampler already kept it; count it, keep.
//	ratioKeep == false → keep (and count) only if fewer than min traces have been
//	                     kept for this (project, op, bucket) — the floor rescue.
//
// bucket is the wall-clock bucket index of the trace's root span
// (start_time_us / bucket width; 60_000_000 for a minute floor). Counting ratio-keeps toward the bucket avoids
// storing an extra floor trace when the ratio sampler already met the minimum.
func (f *MinuteFloor) ShouldKeep(projectID int64, op string, bucket int64, min int, ratioKeep bool) bool {
	key := bucketKey(projectID, op, bucket)

	f.mu.Lock()
	defer f.mu.Unlock()

	b := f.buckets[key]
	if b == nil {
		b = &floorBucket{index: bucket}
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

func bucketKey(projectID int64, op string, bucket int64) string {
	// Compact, allocation-light key. \x00 separators avoid collisions between
	// e.g. op="a\x001" and project boundaries.
	return strconv.FormatInt(projectID, 10) + "\x00" + op + "\x00" + strconv.FormatInt(bucket, 10)
}

func (f *MinuteFloor) gcLoop() {
	ticker := time.NewTicker(floorGCInterval)
	defer ticker.Stop()
	for t := range ticker.C {
		f.gc(t)
	}
}

// gc removes buckets older than retainBuckets relative to now, measured in the
// floor's own bucket width.
// Exposed (unexported but directly callable) so tests can drive it deterministically.
func (f *MinuteFloor) gc(now time.Time) {
	cutoff := now.UnixMicro()/f.width().Microseconds() - f.retainBuckets()
	f.mu.Lock()
	for key, b := range f.buckets {
		if b.index < cutoff {
			delete(f.buckets, key)
		}
	}
	f.mu.Unlock()
}
