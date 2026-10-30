package ingest

import (
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spanledger"
)

// ledgerSum accumulates what a tally reports, the way the stored rows would.
type ledgerSum struct {
	model.SpanLedgerRow
}

func (s *ledgerSum) take(t *spanledger.Tally) {
	for _, r := range t.Take() {
		s.Accepted += r.Accepted
		s.SampledOut += r.SampledOut
		s.ShedUnsampled += r.ShedUnsampled
		s.LostInBuffer += r.LostInBuffer
		s.Forwarded += r.Forwarded
	}
}

// exits is what has left the buffer.
func (s *ledgerSum) exits() int64 {
	return s.SampledOut + s.ShedUnsampled + s.LostInBuffer + s.Forwarded
}

func TestLedgerCountsKeptAndDroppedTraces(t *testing.T) {
	tb := rescueBuffer(RescueRules{})
	tally := spanledger.New()
	tb.SetLedger(tally)

	errored := rootSpan("err-trace", "GET /a", 1000)
	errored.Status = "error"
	tb.Add(errored)
	tb.Add(rootSpan(unsampledID(0), "GET /b", 1000))
	tb.Flush(time.Now().Add(time.Hour))
	drain(tb)

	var sum ledgerSum
	sum.take(tally)
	if sum.Accepted != 2 || sum.Forwarded != 1 || sum.SampledOut != 1 {
		t.Errorf("counts = %+v, want accepted 2, forwarded 1, sampled out 1", sum.SpanLedgerRow)
	}
}

func TestLedgerCountsLateSpanOfDroppedTrace(t *testing.T) {
	tb := rescueBuffer(RescueRules{})
	tally := spanledger.New()
	tb.SetLedger(tally)

	tb.Add(rootSpan(unsampledID(0), "GET /b", 1000))
	tb.Flush(time.Now().Add(time.Second))
	tb.Add(childSpan(unsampledID(0), "late", "OK"))

	var sum ledgerSum
	sum.take(tally)
	if sum.Accepted != 2 || sum.SampledOut != 2 {
		t.Errorf("counts = %+v, want accepted 2, sampled out 2", sum.SpanLedgerRow)
	}
}

// A dropped trace that goes out as the carrier of its operation's drops counts
// as forwarded and no longer as sampled out.
func TestLedgerTakesBackTheExitOfACarrier(t *testing.T) {
	tb := rescueBuffer(RescueRules{})
	tally := spanledger.New()
	tb.SetLedger(tally)
	at := carryBase()

	tb.Add(rootSpan(unsampledID(0), "GET /quiet", 1000))
	tb.Flush(at)
	var sum ledgerSum
	sum.take(tally)
	if sum.SampledOut != 1 || sum.Forwarded != 0 {
		t.Fatalf("after the drop: %+v, want sampled out 1", sum.SpanLedgerRow)
	}

	tb.Flush(at.Add(carryWait))
	drain(tb)
	sum.take(tally)
	if sum.SampledOut != 0 || sum.Forwarded != 1 {
		t.Errorf("after the carrier: %+v, want forwarded 1, sampled out 0", sum.SpanLedgerRow)
	}
}

func TestLedgerCountsEvictionAtTheCap(t *testing.T) {
	tb, at := capRescueBuffer(t, RescueRules{})
	tally := spanledger.New()
	tb.SetLedger(tally)

	for i := range 21 {
		tb.Add(rootSpan(unsampledID(i), "GET /health/ready", 100))
	}
	var sum ledgerSum
	sum.take(tally)
	if sum.Accepted != 21 || sum.ShedUnsampled != 11 {
		t.Errorf("counts = %+v, want accepted 21, shed 11", sum.SpanLedgerRow)
	}

	tb.Close(at)
	for range tb.Out { // closed by Close
	}
	sum.take(tally)
	if sum.exits() != sum.Accepted {
		t.Errorf("after Close: %+v, want every span to have one exit", sum.SpanLedgerRow)
	}
}

// Traces the ratio would keep are lost when eviction takes them, which the
// ledger has to name as loss.
func TestLedgerCountsLossOfKeptTraces(t *testing.T) {
	at := time.Now().Truncate(time.Hour).Add(time.Minute)
	tb := NewTraceBufferWithRules(time.Hour, 2, RescueRules{}, NewStaticRatioLookup(1), testLogger())
	tb.now = func() time.Time { return at }
	tally := spanledger.New()
	tb.SetLedger(tally)

	for i := range 5 {
		tb.Add(rootSpan(unsampledID(i), "GET /kept", 100))
	}
	var sum ledgerSum
	sum.take(tally)
	if sum.Accepted != 5 || sum.LostInBuffer != 3 {
		t.Errorf("counts = %+v, want accepted 5, lost 3", sum.SpanLedgerRow)
	}

	tb.Close(at)
	for range tb.Out { // closed by Close
	}
	sum.take(tally)
	if sum.exits() != sum.Accepted {
		t.Errorf("after Close: %+v, want every span to have one exit", sum.SpanLedgerRow)
	}
}

// Without a ledger the buffer runs as before.
func TestBufferRunsWithoutALedger(t *testing.T) {
	tb := rescueBuffer(RescueRules{})
	tb.Add(rootSpan(unsampledID(0), "GET /b", 1000))
	tb.Flush(time.Now().Add(time.Second))
	if len(drain(tb)) != 0 {
		t.Error("an unsampled trace was delivered")
	}
}
