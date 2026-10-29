package ingest

import (
	"cmp"
	"context"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// DefaultRootGrace is how long the buffer waits after a trace's root span
// arrives before it decides the trace. The root span ends last and is
// exported last, so most of a trace is in by then; the spans that are not
// follow the decision (see trace_buffer_decided.go). Waiting the whole TTL
// instead made every chart run ten minutes late, and every rollout showed a
// burst from the old pod and a ten-minute hole from the new one (#451).
const DefaultRootGrace = 30 * time.Second

// SetRootGrace sets how long after its root span a trace is decided. 0 or
// less waits the whole TTL for every trace.
func (tb *TraceBuffer) SetRootGrace(d time.Duration) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.rootGrace = max(d, 0)
}

// RootGrace returns how long after its root span a trace is decided.
func (tb *TraceBuffer) RootGrace() time.Duration {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.rootGrace
}

// due reports whether a trace is ready for its decision: rootGrace after its
// root span arrived, or the TTL after its first span when no root came.
// Caller holds the lock.
func (tb *TraceBuffer) due(tr *bufferedTrace, now time.Time) bool {
	if tb.rootGrace > 0 && tr.hasRoot() && now.Sub(tr.rootSeen) >= tb.rootGrace {
		return true
	}
	return now.Sub(tr.firstSeen) >= tb.ttl
}

// sortByArrival orders traces as they arrived. A dropped clean trace is carried
// by the next kept trace of its operation, so traces are decided in arrival
// order: ranging over the trace map would make the carrier a matter of map
// order.
func sortByArrival(trs []*bufferedTrace) {
	slices.SortFunc(trs, func(a, b *bufferedTrace) int { return cmp.Compare(a.seq, b.seq) })
}

// settle records the decisions made outside the lock and applies them to the
// spans that arrived meanwhile. It returns the late spans to deliver.
func (tb *TraceBuffer) settle(trs []*bufferedTrace, weights []int, now time.Time) []model.SpanRecord {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	for i, tr := range trs {
		for _, rec := range tb.decided.put(tr.id, weights[i], false, now) {
			tb.decided.follow(rec)
		}
	}
	return tb.decided.takeLate()
}

// keepWeight decides whether the trace goes to storage and returns the sample
// weight of its spans: how many traces it stands for. 0 drops the trace; the
// caller then counts the drop (carry.drop). now is the flush time, used to
// place the trace in an hourly rarity bucket.
//
// The error and slow-root rules keep every trace of their kind, so such a
// trace stands for itself. A clean trace kept as a sample, by the ratio or by
// the hourly floor, stands for itself and for the clean traces of its
// operation dropped since the previous such trace, so every trace is counted
// once whatever the ratio (#333).
//
// The ratio decision is a hash of the trace ID against the ratio of the
// trace's project and root operation, so two pods that each hold part of a
// trace, as during a rollout, decide it the same way. A part without the root
// span asks for the project's ratio.
func (tb *TraceBuffer) keepWeight(tr *bufferedTrace, now time.Time) int {
	if tr.hasError {
		return 1
	}
	if len(tr.spans) == 0 {
		return 0
	}
	op := traceOp(tr)
	if tb.slowRoot(tr) {
		return 1
	}
	ratio := tb.ratio(tr.projectID, tr.rootOp)
	sampled := ratioSampled(tr.id, ratio)
	if !tb.rescued(tr.projectID, op, now, sampled) {
		return 0
	}
	if !tr.hasRoot() {
		return rootlessWeight(sampled, ratio)
	}
	return int(1 + tb.carry.take(tr.projectID, op))
}

// rootlessWeight is the weight of a kept trace whose root span this pod never
// saw. Its drops are not carried (see carryTally.drop): the root may be on
// another pod, which counts the trace there. A part kept by the ratio stands
// for the ratio's worth of such parts instead, and one kept by the floor
// stands for itself.
func rootlessWeight(sampled bool, ratio int) int {
	if sampled {
		return max(ratio, 1)
	}
	return 1
}

// ratio returns the 1-in-N sample ratio for a root operation.
func (tb *TraceBuffer) ratio(projectID int64, op string) int {
	if tb.lookup == nil {
		return DefaultSampleRatio
	}
	return tb.lookup.Ratio(context.Background(), projectID, op)
}

// sampledIn reports whether ratio sampling keeps this trace. Deterministic in
// the trace ID, so the same trace decides the same way on every pod and at
// every call site.
func (tb *TraceBuffer) sampledIn(projectID int64, op, traceID string) bool {
	return ratioSampled(traceID, tb.ratio(projectID, op))
}

func ratioSampled(traceID string, ratio int) bool {
	if ratio <= 1 {
		return true
	}
	return traceIDSampledByRatio(traceID, ratio)
}

// traceIDSampledByRatio returns true for 1/ratio of trace IDs deterministically.
// Uses the same algorithm as the Go self-sampler and JS frontend sampler.
func traceIDSampledByRatio(traceID string, ratio int) bool {
	if len(traceID) < 16 {
		return true
	}
	b, err := hex.DecodeString(traceID[:16])
	if err != nil || len(b) < 8 {
		return true
	}
	upper := binary.BigEndian.Uint64(b)
	return int(upper%uint64(ratio)) == 0
}

func isErrorStatus(s string) bool {
	// OTel status codes: "error", "ERROR", "Error", or numeric code 2
	switch s {
	case "error", "ERROR", "Error", "2":
		return true
	}
	return false
}
