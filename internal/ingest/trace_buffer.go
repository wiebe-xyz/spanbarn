package ingest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/observability"
	"github.com/wiebe-xyz/spanbarn/internal/sampling"
)

const (
	// DefaultTraceBufferTTL is how long a trace whose root span never arrives
	// is buffered before a sampling decision is made, and how long a decision
	// is held for spans that arrive after it. A trace with a root is decided
	// DefaultRootGrace after the root arrived.
	DefaultTraceBufferTTL = 10 * time.Minute

	// DefaultSampleRatio keeps 1 in every N traces. 1000 means 0.1%.
	DefaultSampleRatio = 1000

	// gcInterval is how often the buffer decides the traces that came due. It
	// bounds how late after rootGrace a trace is released.
	gcInterval = 5 * time.Second

	// outDeliverTimeout is how long Flush will wait for the drain goroutine to
	// accept a kept trace before giving up on it. Bounded so the gc loop cannot
	// wedge behind a stalled consumer, but long enough that a brief drain hiccup
	// doesn't cost us error traces.
	outDeliverTimeout = 2 * time.Second

	// DefaultMaxBufferedSpans bounds how many spans are held across all in-flight
	// traces. Every span waits for its trace's decision, so the buffer holds
	// the root grace's worth of *unsampled* traffic, and the TTL's worth for
	// traces without a root — including self-instrument spans, whose volume
	// scales with writer load. An unbounded
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

// TraceBuffer holds traces in memory until DefaultRootGrace after their root
// span arrived, or for the TTL when no root comes, then applies ratio-based
// sampling at flush time. Error traces always pass through intact. Spans that
// arrive after the decision follow it (see trace_buffer_decided.go).
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
	seq    uint64 // arrivals so far; numbers bufferedTrace.seq
	ttl    time.Duration
	lookup SampleRatioLookup
	logger *slog.Logger

	// maxSpans caps spanCount, the number of spans buffered across all traces.
	// 0 disables the cap. spanCount may overshoot slightly: spans on traces
	// known to contain an error are always accepted (see Add).
	maxSpans  int
	pressure  capPressure // how long spanCount has been at maxSpans
	spanCount int

	// rescue keeps low-volume and slow root operations that ratio sampling
	// would drop. See trace_buffer_rescue.go.
	rescue RescueRules
	floor  *sampling.MinuteFloor

	// carry counts the clean traces the buffer drops, so the next trace kept
	// as a sample of their operation counts them. See trace_buffer_carry.go.
	carry *carryTally

	// Eviction order. sacrificeQ holds traces the ratio sample skips. Eviction
	// gives each its flush-time decision first (rescueAtCap), so freeing one
	// costs nothing the slow-root and floor rules would keep. anyQ holds every trace
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

	// rescuedAtCap holds traces that eviction picked but the slow-root or
	// hourly floor rule keeps, weighted and waiting for the next flush.
	rescuedAtCap  []*bufferedTrace
	evictedRescue int64
	evictedRTotal int64

	// rootGrace is how long after its root span arrived a trace is decided.
	// See trace_buffer_decide.go.
	rootGrace time.Duration

	// decided holds the decisions of the last TTL, which late spans follow.
	// See trace_buffer_decided.go.
	decided *decidedTraces

	// now is the clock for arrival times and for decisions made at eviction.
	// Tests replace it.
	now func() time.Time

	// Accepted spans are sent to this channel for the caller to drain. Close
	// closes it.
	Out <-chan []model.SpanRecord

	out chan []model.SpanRecord

	// stop ends the gc loop, which closes gcDone on its way out. See Close.
	stop      chan struct{}
	gcDone    chan struct{}
	closeOnce sync.Once
}

type bufferedTrace struct {
	id          string
	spans       []model.SpanRecord
	hasError    bool
	rootOp      string // operation name of the root span (no parentSpanID)
	projectID   int64
	firstSeen   time.Time
	rootSeen    time.Time // when the root span arrived; zero until then
	sacrificial bool      // the ratio sample skips it; evict this one first
	seq         uint64    // arrival order among traces
}

// hasRoot reports whether the trace's root span arrived.
func (tr *bufferedTrace) hasRoot() bool { return !tr.rootSeen.IsZero() }

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
	// RescuedAtCapSpans is spans of traces eviction picked that the slow-root
	// or hourly floor rule kept instead. They are stored, not lost.
	RescuedAtCapSpans int64
	// UndeliveredSpans is spans that survived sampling but could not be handed
	// to the drain goroutine in time.
	UndeliveredSpans int64
	// CarrierSpans is spans of dropped traces held to be stored as the
	// carrier of their operation's drop count if no sampled trace comes.
	CarrierSpans int
	// CarriedDrops is dropped traces counted into a kept trace's weight.
	CarriedDrops int64
	// UncarriedDrops is dropped traces that waited out carryWait with no
	// carrier held for them, so no stored weight counts them.
	UncarriedDrops int64
	// DecidedTraces is the decisions held for late spans to follow.
	DecidedTraces int
	// DecidedEvicted is decisions the cap dropped before the TTL. A late span
	// of such a trace is decided again on its own.
	DecidedEvicted int64
	// LateSpansKept and LateSpansDropped are spans that arrived after their
	// trace was decided and followed the decision.
	LateSpansKept    int64
	LateSpansDropped int64
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
	return NewTraceBufferWithRules(ttl, maxSpans, RescueRules{}, lookup, logger)
}

// NewTraceBufferWithRules is NewTraceBufferWithLimits plus the rules that keep
// slow and low-volume root operations through ratio sampling.
func NewTraceBufferWithRules(ttl time.Duration, maxSpans int, rules RescueRules, lookup SampleRatioLookup, logger *slog.Logger) *TraceBuffer {
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
		traces:    make(map[string]*bufferedTrace),
		ttl:       ttl,
		lookup:    lookup,
		logger:    logger,
		maxSpans:  maxSpans,
		rootGrace: DefaultRootGrace,
		decided:   newDecidedTraces(ttl, DefaultMaxDecidedTraces),
		rescue:    rules.normalised(),
		carry:     newCarryTally(maxSpans),
		out:       ch,
		Out:       ch,
		stop:      make(chan struct{}),
		gcDone:    make(chan struct{}),
		now:       time.Now,
	}
	if tb.rescue.MinTracesPerHour > 0 {
		tb.floor = sampling.NewBucketFloor(time.Hour)
	}
	observability.SafeGo("trace-buffer-gc", nil, tb.gcLoop)
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
	if tr == nil && tb.decided.follow(rec) {
		return
	}
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
	// eviction hint was made without knowing the operation. Its arrival also
	// starts the grace period after which the trace is decided.
	if rec.ParentSpanID == "" && !tr.hasRoot() {
		tr.rootSeen = tb.now()
		if tr.rootOp == "" {
			tr.rootOp = rec.Name
			tb.relabel(tr)
		}
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
	tb.seq++
	tr := &bufferedTrace{
		id:          rec.TraceID,
		seq:         tb.seq,
		rootOp:      op,
		projectID:   rec.ProjectID,
		firstSeen:   tb.now(),
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
	held, carried, uncarried := tb.carry.stats()
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return BufferStats{
		CarrierSpans:            held,
		CarriedDrops:            carried,
		UncarriedDrops:          uncarried,
		BufferedSpans:           tb.spanCount,
		BufferedTraces:          len(tb.traces),
		MaxSpans:                tb.maxSpans,
		RefusedSpans:            tb.refusedTotal,
		EvictedSacrificialSpans: tb.evictedSTotal,
		EvictedKeptSpans:        tb.evictedKTotal,
		RescuedAtCapSpans:       tb.evictedRTotal,
		UndeliveredSpans:        tb.undeliverTotal,
		DecidedTraces:           len(tb.decided.m),
		DecidedEvicted:          tb.decided.evicted,
		LateSpansKept:           tb.decided.lateKept,
		LateSpansDropped:        tb.decided.lateDropped,
	}
}

// Flush evaluates and emits all traces that came due: rootGrace after their
// root span, or the TTL after their first span when no root came.
// Called by the background gc loop; also callable manually in tests.
func (tb *TraceBuffer) Flush(now time.Time) {
	tb.flush(now, false)
}

// flush decides the traces that came due, or every buffered trace when all is
// set, and returns how many spans it sent to Out.
func (tb *TraceBuffer) flush(now time.Time, all bool) (sent int) {
	tb.mu.Lock()
	toDecide := make([]*bufferedTrace, 0, 16)
	for _, tr := range tb.traces {
		if all || tb.due(tr, now) {
			toDecide = append(toDecide, tr)
		}
	}
	sortByArrival(toDecide)
	// Move due traces out of the map before releasing the lock, releasing
	// their span budget back to the cap as they go. A span that arrives while
	// the trace is decided waits for the decision (see settle).
	for _, tr := range toDecide {
		tb.remove(tr)
		tb.decided.put(tr.id, 0, true, now)
	}
	tb.pruneEvictionQueues()
	tb.decided.expire(now)
	kept := tb.rescuedAtCap
	tb.rescuedAtCap = nil
	loss := tb.takeLossWindow()
	loss.buffered, loss.traces = tb.spanCount, len(tb.traces)
	loss.atCapFor = tb.pressure.observe(loss, now)
	tb.mu.Unlock()

	tb.reportLoss(loss)

	weights := make([]int, len(toDecide))
	for i, tr := range toDecide {
		if weights[i] = tb.keepWeight(tr, now); weights[i] > 0 {
			stampWeight(tr, weights[i])
			kept = append(kept, tr)
		} else {
			tb.carry.drop(tr, now)
		}
	}
	kept = append(kept, tb.carry.expired(now, all)...)
	late := tb.settle(toDecide, weights, now)
	return tb.deliverAll(kept, late)
}

// deliverAll sends the kept traces and late spans to Out and returns how many
// spans it sent.
func (tb *TraceBuffer) deliverAll(kept []*bufferedTrace, late []model.SpanRecord) (sent int) {
	batches := make([][]model.SpanRecord, 0, len(kept)+1)
	for _, tr := range kept {
		batches = append(batches, tr.spans)
	}
	if len(late) > 0 {
		batches = append(batches, late)
	}
	var undelivered int64
	for _, spans := range batches {
		// A trace that survived sampling has already been paid for; dropping it
		// here silently would discard error traces too — the one guarantee the
		// buffer makes. Give the drain a bounded chance to catch up, then count
		// and report what we lose instead of losing it invisibly.
		if tb.deliver(spans) {
			sent += len(spans)
		} else {
			undelivered += int64(len(spans))
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
	return sent
}

// deliver hands spans to the drain goroutine, waiting at most
// outDeliverTimeout for it to make room.
func (tb *TraceBuffer) deliver(spans []model.SpanRecord) bool {
	select {
	case tb.out <- spans:
		return true
	default:
	}
	timer := time.NewTimer(outDeliverTimeout)
	defer timer.Stop()
	select {
	case tb.out <- spans:
		return true
	case <-timer.C:
		return false
	}
}

func (tb *TraceBuffer) gcLoop() {
	defer close(tb.gcDone)
	ticker := time.NewTicker(gcInterval)
	defer ticker.Stop()
	for {
		select {
		case <-tb.stop:
			return
		case t := <-ticker.C:
			tb.Flush(t)
		}
	}
}
