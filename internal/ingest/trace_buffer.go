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
	//
	// Tune with SPANBARN_TRACE_BUFFER_MAX_SPANS when a deployment's real span
	// rate needs more headroom than this default allows.
	DefaultMaxBufferedSpans = 50_000

	// evictQueueCompactAt is the number of consumed entries an eviction queue
	// tolerates before its backing array is compacted.
	evictQueueCompactAt = 1024
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
//
// At capacity the buffer *evicts* rather than refusing admission. Refusing new
// traces looks even-handed but is not: a service that emits continuously keeps
// whatever it manages to squeeze in, while a one-shot producer — a CronJob, a
// CLI, a serverless invocation — emits its whole trace in a single batch and so
// loses the entire run. Eviction spreads the same pressure across every trace
// already buffered, so a trace's odds no longer depend on how its spans are
// spread over time.
type TraceBuffer struct {
	mu     sync.Mutex
	traces map[string]*bufferedTrace
	ttl    time.Duration
	lookup SampleRatioLookup
	logger *slog.Logger

	// maxSpans caps spanCount, the number of spans buffered across all traces.
	// 0 disables the cap. spanCount may overshoot slightly: spans on traces
	// known to contain an error are always accepted (see Add).
	maxSpans  int
	spanCount int

	// Eviction order. sacrificeQ holds traces whose flush-time sampling decision
	// is already known to be "discard" — freeing one of those costs nothing at
	// all, because it was never going to reach storage. anyQ holds every trace
	// in insertion order and is the fallback for when nothing is sacrificial.
	// Entries are skipped lazily: a queued trace may since have been flushed,
	// evicted, or latched as an error trace.
	sacrificeQ    []string
	sacrificeHead int
	anyQ          []string
	anyHead       int

	// Loss counters. Windowed values feed the flush log and reset each Flush;
	// the totals are cumulative and feed Stats.
	refused        int64
	evictedSacrif  int64
	evictedKept    int64
	refusedTotal   int64
	evictedSTotal  int64
	evictedKTotal  int64
	undeliverTotal int64

	// Accepted spans are sent to this channel for the caller to drain.
	Out <-chan []model.SpanRecord

	out chan []model.SpanRecord
}

type bufferedTrace struct {
	id          string
	spans       []model.SpanRecord
	hasError    bool
	rootOp      string // operation name of the root span (no parentSpanID)
	projectID   int64
	firstSeen   time.Time
	sacrificial bool // sampling already says "discard"; evict this one first
}

// BufferStats is a point-in-time view of the buffer, for metrics and health
// reporting. The drop counters are cumulative since process start.
type BufferStats struct {
	BufferedSpans  int
	BufferedTraces int
	MaxSpans       int
	// RefusedSpans is spans the buffer could not admit at all — it was at cap
	// and held nothing evictable. Non-zero means real, unavoidable loss.
	RefusedSpans int64
	// EvictedSacrificialSpans is spans freed from traces that sampling was
	// going to discard at flush anyway. This is the cap doing its job; it costs
	// no stored telemetry.
	EvictedSacrificialSpans int64
	// EvictedKeptSpans is spans freed from traces that *would* have been kept.
	// Non-zero means the cap is genuinely too small for the span rate.
	EvictedKeptSpans int64
	// UndeliveredSpans is spans that survived sampling but could not be handed
	// to the drain goroutine in time.
	UndeliveredSpans int64
}

// NewTraceBuffer creates a buffer with the given TTL and ratio lookup, capped at
// DefaultMaxBufferedSpans.
func NewTraceBuffer(ttl time.Duration, lookup SampleRatioLookup, logger *slog.Logger) *TraceBuffer {
	return NewTraceBufferWithLimits(ttl, DefaultMaxBufferedSpans, lookup, logger)
}

// NewTraceBufferWithLimits creates a buffer with an explicit span cap.
// A maxSpans of 0 or less disables the cap entirely — only safe where the span
// rate is known to be bounded, since buffered spans are live data the GC cannot
// reclaim for the whole TTL.
func NewTraceBufferWithLimits(ttl time.Duration, maxSpans int, lookup SampleRatioLookup, logger *slog.Logger) *TraceBuffer {
	if logger == nil {
		logger = slog.Default()
	}
	if ttl <= 0 {
		ttl = DefaultTraceBufferTTL
	}
	if maxSpans < 0 {
		maxSpans = 0
	}
	ch := make(chan []model.SpanRecord, 256)
	tb := &TraceBuffer{
		traces:   make(map[string]*bufferedTrace),
		ttl:      ttl,
		lookup:   lookup,
		logger:   logger,
		maxSpans: maxSpans,
		out:      ch,
		Out:      ch,
	}
	go tb.gcLoop()
	return tb
}

// Add buffers a span. The span's error status is checked immediately so that
// even if later spans arrive after a flush, the error is not lost.
//
// At capacity the buffer makes room by evicting (see ensureRoom) rather than
// turning the span away. A span is only refused when the buffer is full of
// traces that may not be evicted — that is, error traces — and the span itself
// is not part of one.
func (tb *TraceBuffer) Add(rec model.SpanRecord) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	isErr := isErrorStatus(rec.Status)
	tr := tb.traces[rec.TraceID]
	protected := isErr || (tr != nil && tr.hasError)

	// Error traces bypass the cap: they are the traces the buffer promises to
	// keep, and that promise has to hold at cap too — including for an error
	// trace the buffer is seeing for the first time.
	if !tb.ensureRoom(tr) && !protected {
		tb.refused++
		tb.refusedTotal++
		return
	}

	if tr == nil {
		tr = tb.newTrace(rec)
	}

	// Latch the error status: even if later spans of this trace are dropped,
	// the trace must still be recognised as an error trace so keep() passes
	// whatever spans of it we did retain.
	if isErr {
		tr.hasError = true
	}

	tr.spans = append(tr.spans, rec)
	tb.spanCount++

	// The root span has no parent; use its name as the representative operation
	// for the ratio lookup. If we see it, latch it — and re-decide, since the
	// eviction hint was made without knowing the operation.
	if rec.ParentSpanID == "" && tr.rootOp == "" {
		tr.rootOp = rec.Name
		tb.relabel(tr)
	}
}

// relabel recomputes a trace's eviction hint now that its root operation is
// known. A trace can only gain protection here or lose it; nextVictim re-checks
// the flag, so a stale sacrificeQ entry is harmless. Caller holds the lock.
func (tb *TraceBuffer) relabel(tr *bufferedTrace) {
	was := tr.sacrificial
	tr.sacrificial = !tb.sampledIn(tr.projectID, tr.rootOp, tr.id)
	if tr.sacrificial && !was {
		tb.sacrificeQ = append(tb.sacrificeQ, tr.id)
	}
}

// newTrace registers a new buffered trace and queues it for eviction. Caller
// holds the lock.
func (tb *TraceBuffer) newTrace(rec model.SpanRecord) *bufferedTrace {
	// The root operation is only known if this first span happens to be the
	// root; otherwise the lookup falls back to the project/global ratio. That
	// is fine — this decision only orders eviction, it never decides whether a
	// trace is stored. keep() re-decides authoritatively at flush time.
	op := ""
	if rec.ParentSpanID == "" {
		op = rec.Name
	}
	tr := &bufferedTrace{
		id:          rec.TraceID,
		rootOp:      op,
		projectID:   rec.ProjectID,
		firstSeen:   time.Now(),
		sacrificial: !tb.sampledIn(rec.ProjectID, op, rec.TraceID),
	}
	tb.traces[rec.TraceID] = tr
	tb.anyQ = append(tb.anyQ, rec.TraceID)
	if tr.sacrificial {
		tb.sacrificeQ = append(tb.sacrificeQ, rec.TraceID)
	}
	return tr
}

// Stats returns a point-in-time view of the buffer.
func (tb *TraceBuffer) Stats() BufferStats {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return BufferStats{
		BufferedSpans:           tb.spanCount,
		BufferedTraces:          len(tb.traces),
		MaxSpans:                tb.maxSpans,
		RefusedSpans:            tb.refusedTotal,
		EvictedSacrificialSpans: tb.evictedSTotal,
		EvictedKeptSpans:        tb.evictedKTotal,
		UndeliveredSpans:        tb.undeliverTotal,
	}
}

// Flush evaluates and emits all traces whose TTL has expired.
// Called by the background gc loop; also callable manually in tests.
func (tb *TraceBuffer) Flush(now time.Time) {
	tb.mu.Lock()
	toDecide := make([]*bufferedTrace, 0, 16)
	for _, tr := range tb.traces {
		if now.Sub(tr.firstSeen) >= tb.ttl {
			toDecide = append(toDecide, tr)
		}
	}
	// Move expired traces out of the map before releasing the lock, releasing
	// their span budget back to the cap as they go.
	for _, tr := range toDecide {
		tb.remove(tr)
	}
	tb.pruneEvictionQueues()
	loss := tb.takeLossWindow()
	loss.buffered, loss.traces = tb.spanCount, len(tb.traces)
	tb.mu.Unlock()

	tb.reportLoss(loss)

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
		tb.mu.Lock()
		tb.undeliverTotal += undelivered
		tb.mu.Unlock()
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
	if len(tr.spans) == 0 {
		return false
	}
	op := tr.rootOp
	if op == "" {
		op = tr.spans[0].Name
	}
	return tb.sampledIn(tr.projectID, op, tr.id)
}

// sampledIn reports whether ratio sampling keeps this trace. Deterministic in
// the trace ID, so the same trace decides the same way on every pod and at
// every call site.
func (tb *TraceBuffer) sampledIn(projectID int64, op, traceID string) bool {
	ratio := DefaultSampleRatio
	if tb.lookup != nil {
		ratio = tb.lookup.Ratio(context.Background(), projectID, op)
	}
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

func (tb *TraceBuffer) gcLoop() {
	ticker := time.NewTicker(gcInterval)
	defer ticker.Stop()
	for t := range ticker.C {
		tb.Flush(t)
	}
}
