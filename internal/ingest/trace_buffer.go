package ingest

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	// DefaultTraceBufferTTL is how long a trace is buffered before a sampling
	// decision is made. Long enough to collect all spans; short enough that
	// errors are visible within a reasonable window.
	DefaultTraceBufferTTL = 10 * time.Minute

	// DefaultSampleRatio keeps 1 in every N traces. 1000 means 0.1%.
	DefaultSampleRatio = 1000

	gcInterval = 30 * time.Second
)

// SampleRatioLookup returns the configured ratio for a (projectID, operation)
// pair. A ratio of N means keep 1 in every N traces. 1 means keep all.
// Implementations are expected to cache aggressively; this is called on every
// trace flush.
type SampleRatioLookup interface {
	Ratio(ctx context.Context, projectID int64, operation string) int
}

// TraceBuffer holds complete traces in memory for up to TTL, then applies
// ratio-based sampling at flush time. Error traces always pass through intact.
//
// The sampling decision for a trace is deterministic: it uses the first 8
// bytes of the trace ID as a uint64, then checks value % ratio == 0. The
// same trace ID always produces the same decision regardless of which pod
// processes it.
type TraceBuffer struct {
	mu     sync.Mutex
	traces map[string]*bufferedTrace
	ttl    time.Duration
	lookup SampleRatioLookup

	// Accepted spans are sent to this channel for the caller to drain.
	Out <-chan []model.SpanRecord

	out chan []model.SpanRecord
}

type bufferedTrace struct {
	spans     []model.SpanRecord
	hasError  bool
	rootOp    string // operation name of the root span (no parentSpanID)
	projectID int64
	firstSeen time.Time
}

// NewTraceBuffer creates a buffer with the given TTL and ratio lookup.
func NewTraceBuffer(ttl time.Duration, lookup SampleRatioLookup) *TraceBuffer {
	ch := make(chan []model.SpanRecord, 256)
	tb := &TraceBuffer{
		traces: make(map[string]*bufferedTrace),
		ttl:    ttl,
		lookup: lookup,
		out:    ch,
		Out:    ch,
	}
	go tb.gcLoop()
	return tb
}

// Add buffers a span. The span's error status is checked immediately so that
// even if later spans arrive after a flush, the error is not lost.
func (tb *TraceBuffer) Add(rec model.SpanRecord) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tr := tb.traces[rec.TraceID]
	if tr == nil {
		tr = &bufferedTrace{
			projectID: rec.ProjectID,
			firstSeen: time.Now(),
		}
		tb.traces[rec.TraceID] = tr
	}
	tr.spans = append(tr.spans, rec)

	if isErrorStatus(rec.Status) {
		tr.hasError = true
	}

	// The root span has no parent; use its name as the representative operation
	// for the ratio lookup. If we see it, latch it.
	if rec.ParentSpanID == "" && tr.rootOp == "" {
		tr.rootOp = rec.Name
	}
}

// Flush evaluates and emits all traces whose TTL has expired.
// Called by the background gc loop; also callable manually in tests.
func (tb *TraceBuffer) Flush(now time.Time) {
	tb.mu.Lock()
	var expired []string
	for id, tr := range tb.traces {
		if now.Sub(tr.firstSeen) >= tb.ttl {
			expired = append(expired, id)
		}
	}
	// Move expired traces out of the map before releasing the lock.
	toDecide := make([]*bufferedTrace, 0, len(expired))
	for _, id := range expired {
		toDecide = append(toDecide, tb.traces[id])
		delete(tb.traces, id)
	}
	tb.mu.Unlock()

	for _, tr := range toDecide {
		if tb.keep(tr) {
			select {
			case tb.out <- tr.spans:
			default:
				// Channel full — drop to avoid blocking the gc goroutine.
			}
		}
	}
}

// keep returns true if the trace should be forwarded to storage.
func (tb *TraceBuffer) keep(tr *bufferedTrace) bool {
	if tr.hasError {
		return true
	}
	op := tr.rootOp
	if op == "" && len(tr.spans) > 0 {
		op = tr.spans[0].Name
	}
	ratio := DefaultSampleRatio
	if tb.lookup != nil {
		ratio = tb.lookup.Ratio(context.Background(), tr.projectID, op)
	}
	if ratio <= 1 {
		return true
	}
	return traceIDSampledByRatio(tr.spans[0].TraceID, ratio)
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

func (tb *TraceBuffer) gcLoop() {
	ticker := time.NewTicker(gcInterval)
	defer ticker.Stop()
	for t := range ticker.C {
		tb.Flush(t)
	}
}
