package ingest

import (
	"github.com/wiebe-xyz/spanbarn/internal/spanledger"
)

// Counting what leaves the buffer (#471).
//
// Every span Add takes in is counted as accepted, and leaves through exactly
// one of: sampled out, shed at the cap, lost in the buffer, or forwarded. A span
// a pod still holds when it dies has no exit, and the ledger's comparison reads
// that as loss.
//
// A dropped trace can come back as the carrier of its operation's drop count
// (trace_buffer_carry.go). Its spans then take back the exit they were counted
// under, so they are counted once.

// SetLedger makes the buffer count its spans in t. Call it before traffic
// arrives.
func (tb *TraceBuffer) SetLedger(t *spanledger.Tally) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.ledger = t
	tb.decided.ledger = t
}

// ledgerDropped counts a trace leaving the buffer undelivered. Call it before
// carry.drop, which can hold the trace as a carrier.
func (tb *TraceBuffer) ledgerDropped(tr *bufferedTrace, col spanledger.Column) {
	tr.droppedAs = col
	tb.ledger.AddSpans(col, tr.spans, 1)
}

// ledgerCarriers takes back the exit of dropped traces that go out as carriers
// and returns them.
func (tb *TraceBuffer) ledgerCarriers(carriers []*bufferedTrace) []*bufferedTrace {
	for _, tr := range carriers {
		tb.ledger.AddSpans(tr.droppedAs, tr.spans, -1)
	}
	return carriers
}
