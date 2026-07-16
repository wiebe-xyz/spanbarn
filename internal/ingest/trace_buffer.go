package ingest

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
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

	// outDeliverTimeout is how long Flush will wait for the drain goroutine to
	// accept a kept trace before giving up on it. Bounded so the gc loop cannot
	// wedge behind a stalled consumer, but long enough that a brief drain hiccup
	// doesn't cost us error traces.
	outDeliverTimeout = 2 * time.Second

	// DefaultMaxBufferedSpans bounds how many spans are held across all in-flight
	// traces. Every span waits out the full TTL before a sampling decision is
	// made, so the buffer holds TTL-worth of *unsampled* traffic — including
	// self-instrument spans, whose volume scales with writer load. An unbounded
	// map therefore grows fastest exactly when the pod is already struggling, so
	// it is capped: the reader runs under a 448MiB GOMEMLIMIT and this map is
	// live data the GC cannot reclaim.
	DefaultMaxBufferedSpans = 50_000
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
	logger *slog.Logger

	// maxSpans caps spanCount, the number of spans buffered across all traces.
	// 0 disables the cap. spanCount may overshoot slightly: spans on traces
	// already known to contain an error are always accepted (see Add).
	maxSpans  int
	spanCount int
	dropped   int64

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

// NewTraceBuffer creates a buffer with the given TTL and ratio lookup, capped at
// DefaultMaxBufferedSpans.
func NewTraceBuffer(ttl time.Duration, lookup SampleRatioLookup, logger *slog.Logger) *TraceBuffer {
	if logger == nil {
		logger = slog.Default()
	}
	ch := make(chan []model.SpanRecord, 256)
	tb := &TraceBuffer{
		traces:   make(map[string]*bufferedTrace),
		ttl:      ttl,
		lookup:   lookup,
		logger:   logger,
		maxSpans: DefaultMaxBufferedSpans,
		out:      ch,
		Out:      ch,
	}
	go tb.gcLoop()
	return tb
}

// Add buffers a span. The span's error status is checked immediately so that
// even if later spans arrive after a flush, the error is not lost.
func (tb *TraceBuffer) Add(rec model.SpanRecord) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	atCap := tb.maxSpans > 0 && tb.spanCount >= tb.maxSpans

	tr := tb.traces[rec.TraceID]
	if tr == nil {
		if atCap {
			tb.dropped++
			return
		}
		tr = &bufferedTrace{
			projectID: rec.ProjectID,
			firstSeen: time.Now(),
		}
		tb.traces[rec.TraceID] = tr
	}

	// Latch the error status before the cap check: even when the span's payload
	// is dropped, the trace must still be recognised as an error trace so keep()
	// passes whatever spans of it we did retain.
	if isErrorStatus(rec.Status) {
		tr.hasError = true
	}

	// Over capacity, keep collecting error traces — they bypass sampling and are
	// the traces worth protecting — but stop growing everything else.
	if atCap && !tr.hasError {
		tb.dropped++
		return
	}

	tr.spans = append(tr.spans, rec)
	tb.spanCount++

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
	// Move expired traces out of the map before releasing the lock, releasing
	// their span budget back to the cap as they go.
	toDecide := make([]*bufferedTrace, 0, len(expired))
	for _, id := range expired {
		tr := tb.traces[id]
		toDecide = append(toDecide, tr)
		tb.spanCount -= len(tr.spans)
		delete(tb.traces, id)
	}
	if tb.spanCount < 0 {
		tb.spanCount = 0
	}
	dropped := tb.dropped
	tb.dropped = 0
	buffered, tracesHeld := tb.spanCount, len(tb.traces)
	tb.mu.Unlock()

	if dropped > 0 {
		tb.logger.Warn("trace buffer over capacity, spans dropped",
			"dropped", dropped,
			"max_spans", tb.maxSpans,
			"buffered_spans", buffered,
			"buffered_traces", tracesHeld)
	}

	var undelivered int64
	for _, tr := range toDecide {
		if !tb.keep(tr) {
			continue
		}
		// A trace that survived sampling has already been paid for; dropping it
		// here silently would discard error traces too — the one guarantee the
		// buffer makes. Give the drain a bounded chance to catch up, then count
		// and report what we lose instead of losing it invisibly.
		select {
		case tb.out <- tr.spans:
		default:
			timer := time.NewTimer(outDeliverTimeout)
			select {
			case tb.out <- tr.spans:
			case <-timer.C:
				undelivered += int64(len(tr.spans))
			}
			timer.Stop()
		}
	}
	if undelivered > 0 {
		tb.logger.Warn("trace buffer output blocked, sampled spans lost",
			"spans", undelivered,
			"hint", "the drain goroutine is not keeping up with kept traces")
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
