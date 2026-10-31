package ingest

import (
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spanledger"
)

// Remembering decisions (#451).
//
// The buffer decides a trace 30 s after its root span arrived, before every
// span of a long trace is in: a span exported late, or by a slow service,
// arrives after the decision. Without a record of the decision such a span
// would start a new trace without a root, wait out the whole TTL and then be
// decided on its own, so a dropped trace could come back as a stored part.
//
// decidedTraces keeps each decision for the TTL. A span of a decided trace
// follows it: it is stored with the trace's weight when the trace was kept,
// and dropped when it was not, unless it carries an error, which keeps it and
// every later span of the trace with weight 1.

// DefaultMaxDecidedTraces bounds the decisions held. An entry costs about 100
// bytes, so the cap costs about 10 MB. At the cap the oldest decision goes
// first, and a late span of that trace is buffered as a new trace again;
// BufferStats.DecidedEvicted counts those.
const DefaultMaxDecidedTraces = 100_000

type decision struct {
	weight  int // 0 for a dropped trace
	seq     uint64
	pending bool // being decided outside the lock
}

type decisionRef struct {
	id  string
	seq uint64
	at  time.Time
}

// decidedTraces is guarded by the buffer's lock.
type decidedTraces struct {
	m     map[string]decision
	order []decisionRef // oldest first; entries replaced since are skipped
	head  int
	seq   uint64
	ttl   time.Duration
	max   int

	held map[string][]model.SpanRecord // spans that arrived while pending
	late []model.SpanRecord            // late spans kept, for the next flush

	ledger *spanledger.Tally

	evicted     int64 // decisions dropped by the cap before the TTL
	lateKept    int64
	lateDropped int64
}

func newDecidedTraces(ttl time.Duration, maxEntries int) *decidedTraces {
	return &decidedTraces{
		m:    map[string]decision{},
		held: map[string][]model.SpanRecord{},
		ttl:  ttl,
		max:  maxEntries,
	}
}

// put records a trace's decision, or marks it pending, and returns the spans
// that arrived while it was pending.
func (d *decidedTraces) put(id string, weight int, pending bool, at time.Time) []model.SpanRecord {
	held := d.held[id]
	delete(d.held, id)
	if e, ok := d.m[id]; ok {
		e.weight, e.pending = weight, pending
		d.m[id] = e
		return held
	}
	d.makeRoom()
	d.seq++
	d.m[id] = decision{weight: weight, seq: d.seq, pending: pending}
	d.order = append(d.order, decisionRef{id: id, seq: d.seq, at: at})
	return held
}

// follow applies the decision on rec's trace to rec and reports whether the
// trace was decided. A kept span waits in late for the next flush.
func (d *decidedTraces) follow(rec model.SpanRecord) bool {
	e, ok := d.m[rec.TraceID]
	if !ok {
		return false
	}
	switch {
	case e.pending:
		d.held[rec.TraceID] = append(d.held[rec.TraceID], rec)
		return true
	case e.weight == 0 && isErrorStatus(rec.Status):
		e.weight = 1
		d.m[rec.TraceID] = e
	case e.weight == 0:
		d.lateDropped++
		d.ledger.Add(spanledger.SampledOut, rec.ProjectID, rec.StartTimeUs, 1)
		return true
	}
	rec.SampleWeight = e.weight
	d.late = append(d.late, rec)
	d.lateKept++
	return true
}

// takeLate returns the late spans kept since the last call.
func (d *decidedTraces) takeLate() []model.SpanRecord {
	late := d.late
	d.late = nil
	return late
}

// expire forgets the decisions older than the TTL.
func (d *decidedTraces) expire(now time.Time) {
	cutoff := now.Add(-d.ttl)
	for d.head < len(d.order) {
		ref := d.order[d.head]
		if d.live(ref) && ref.at.After(cutoff) {
			break
		}
		d.pop()
	}
	d.compact()
}

// makeRoom drops the oldest decisions until one more fits under the cap.
func (d *decidedTraces) makeRoom() {
	for d.max > 0 && len(d.m) >= d.max && d.head < len(d.order) {
		if d.live(d.order[d.head]) {
			d.evicted++
		}
		d.pop()
	}
	d.compact()
}

// live reports whether ref is still the entry its trace ID maps to.
func (d *decidedTraces) live(ref decisionRef) bool {
	e, ok := d.m[ref.id]
	return ok && e.seq == ref.seq
}

// pop removes the oldest ref and, when it is live, its decision.
func (d *decidedTraces) pop() {
	ref := d.order[d.head]
	if d.live(ref) {
		delete(d.m, ref.id)
	}
	d.order[d.head] = decisionRef{}
	d.head++
}

// compact reclaims the consumed prefix of order once it is worth the copy.
func (d *decidedTraces) compact() {
	if d.head < evictQueueCompactAt || d.head*2 < len(d.order) {
		return
	}
	d.order = append(d.order[:0], d.order[d.head:]...)
	d.head = 0
}
