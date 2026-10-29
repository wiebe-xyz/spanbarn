package ingest

import (
	"sync"
	"time"
)

// Counting what the buffer drops (#333).
//
// A kept trace's sample weight says how many traces it stands for. Scaling a
// ratio-sampled trace by the ratio is right on average but wrong for any one
// hour of a low-volume operation: 7 requests read as 1 (the floor trace) in
// most hours and as 1001 in the hour one of them is sampled. So the buffer
// counts the clean traces it drops per (project, root operation) and the next
// trace it keeps as a sample of that operation carries them: its weight is 1
// plus the drops since the previous carrier. Every trace the buffer sees is
// then counted once, by itself or inside a carrier's weight, and the ratio
// only decides which traces are stored.
//
// Error and slow traces are kept by rules that keep every trace of their kind
// and never carry drops, so an SLO that counts good spans does not see clean
// traces inside an error or slow trace's weight.
//
// When no carrier comes within carryWait (an operation that had a burst and
// then went quiet), the buffer stores the latest dropped trace of that
// operation as the carrier. At Close every pending count is stored the same
// way, so a rollout loses none of them.

// carryWait is how long dropped traces wait for a sampled trace of their
// operation before the latest of them is stored as their carrier. The hourly
// floor keeps the first trace of every hour, so an operation with steady
// traffic finds a carrier within about an hour.
const carryWait = time.Hour

// defaultMaxHeldSpans bounds the spans of the dropped traces held as
// carriers when the buffer has no span cap to derive the bound from.
const defaultMaxHeldSpans = 10_000

type carryKey struct {
	projectID int64
	op        string
}

// carryEntry is the uncarried drops of one root operation.
type carryEntry struct {
	dropped int64          // traces dropped since the last carrier
	held    *bufferedTrace // a dropped trace to store as the carrier if none comes
	since   time.Time      // when the oldest uncarried drop happened
}

// carryTally holds the uncarried drops per (project, root operation). It has
// its own lock because the buffer decides traces both under its lock
// (eviction) and outside it (flush).
type carryTally struct {
	mu           sync.Mutex
	m            map[carryKey]*carryEntry
	heldSpans    int
	maxHeldSpans int

	carriedTotal   int64 // dropped traces counted into a carrier's weight
	uncarriedTotal int64 // dropped traces that expired without a carrier
}

func newCarryTally(maxBufferedSpans int) *carryTally {
	limit := maxBufferedSpans / 5
	if limit <= 0 {
		limit = defaultMaxHeldSpans
	}
	return &carryTally{m: map[carryKey]*carryEntry{}, maxHeldSpans: limit}
}

// drop counts a clean trace the buffer is discarding. The trace is held as
// the operation's fallback carrier when it fits the span budget; otherwise
// the trace held before it stays.
//
// A trace whose root span this pod never saw is not counted: during a rollout
// the root is often on the other pod, which counts the trace, and counting
// the part here as well would count it twice. Such parts kept by the ratio
// carry the ratio as their weight instead (rootlessWeight).
func (c *carryTally) drop(tr *bufferedTrace, now time.Time) {
	if len(tr.spans) == 0 || !tr.hasRoot() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := carryKey{tr.projectID, traceOp(tr)}
	e := c.m[k]
	if e == nil {
		e = &carryEntry{since: now}
		c.m[k] = e
	}
	e.dropped++
	held := 0
	if e.held != nil {
		held = len(e.held.spans)
	}
	if c.heldSpans-held+len(tr.spans) <= c.maxHeldSpans {
		c.heldSpans += len(tr.spans) - held
		e.held = tr
	}
}

// take returns the drops a trace kept as a sample of op carries, and clears
// them.
func (c *carryTally) take(projectID int64, op string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := carryKey{projectID, op}
	e := c.m[k]
	if e == nil {
		return 0
	}
	c.release(k, e)
	c.carriedTotal += e.dropped
	return e.dropped
}

// expired returns the held carriers of every operation whose drops waited
// carryWait without a carrier, or of every operation when all is set, each
// weighted with its operation's drop count. Drops without a held trace are
// counted as uncarried.
func (c *carryTally) expired(now time.Time, all bool) []*bufferedTrace {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*bufferedTrace
	for k, e := range c.m {
		if !all && now.Sub(e.since) < carryWait {
			continue
		}
		c.release(k, e)
		if e.held == nil {
			c.uncarriedTotal += e.dropped
			continue
		}
		stampWeight(e.held, int(e.dropped))
		c.carriedTotal += e.dropped
		out = append(out, e.held)
	}
	return out
}

// release removes an entry and returns its held spans to the budget. Caller
// holds c.mu.
func (c *carryTally) release(k carryKey, e *carryEntry) {
	delete(c.m, k)
	if e.held != nil {
		c.heldSpans -= len(e.held.spans)
	}
}

// stats returns the held carrier spans and the cumulative carried and
// uncarried drop counts.
func (c *carryTally) stats() (heldSpans int, carried, uncarried int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.heldSpans, c.carriedTotal, c.uncarriedTotal
}

// traceOp is the root operation a trace is counted under: its root span's
// name, or its first span's when the root never arrived.
func traceOp(tr *bufferedTrace) string {
	if tr.rootOp != "" || len(tr.spans) == 0 {
		return tr.rootOp
	}
	return tr.spans[0].Name
}
