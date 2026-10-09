package ingest

import (
	"time"
)

// DefaultMinTracesPerHour is how many traces per (project, root operation) and
// wall-clock hour the buffer keeps regardless of the sample ratio.
const DefaultMinTracesPerHour = 1

// RescueRules configures what the buffer keeps beyond error traces and the
// ratio sample. Without them stage 1 drops the traces stage 2 (worker
// classification) exists to keep: an operation that runs once a day is dropped
// 999 times in 1000 before the hourly rarity tier ever sees it, and a slow
// trace is dropped before the slow rule can act on it.
type RescueRules struct {
	// SlowThresholdUs keeps every trace whose root span lasted longer than this
	// many microseconds. 0 or less disables the rule.
	SlowThresholdUs int64
	// MinTracesPerHour keeps up to this many traces per (project, root
	// operation) per wall-clock hour, counting traces the ratio sample already
	// kept. 0 or less disables the rule.
	MinTracesPerHour int
}

func (r RescueRules) normalised() RescueRules {
	if r.SlowThresholdUs < 0 {
		r.SlowThresholdUs = 0
	}
	if r.MinTracesPerHour < 0 {
		r.MinTracesPerHour = 0
	}
	return r
}

// slowRoot reports whether the trace's root span exceeded the slow threshold.
func (tb *TraceBuffer) slowRoot(tr *bufferedTrace) bool {
	if tb.rescue.SlowThresholdUs <= 0 {
		return false
	}
	for _, sp := range tr.spans {
		if sp.ParentSpanID == "" && sp.DurationUs > tb.rescue.SlowThresholdUs {
			return true
		}
	}
	return false
}

// rescued applies the hourly rarity floor to a clean trace and returns the
// final keep decision. ratioKeep is the ratio sampler's verdict; a ratio keep
// still counts toward the hour so the floor does not store an extra trace.
func (tb *TraceBuffer) rescued(projectID int64, op string, now time.Time, ratioKeep bool) bool {
	if tb.floor == nil {
		return ratioKeep
	}
	bucket := tb.floor.BucketOf(now.UnixMicro())
	return tb.floor.ShouldKeep(projectID, op, bucket, tb.rescue.MinTracesPerHour, ratioKeep)
}
