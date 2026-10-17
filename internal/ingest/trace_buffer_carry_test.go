package ingest

import (
	"fmt"
	"testing"
	"time"
)

// weightSum adds the root span weights of delivered traces per root operation.
func weightSum(totals map[string]int, traces map[string][]rootWeight) {
	for _, tr := range traces {
		for _, r := range tr {
			totals[r.name] += r.weight
		}
	}
}

type rootWeight struct {
	name   string
	weight int
}

// drainRoots reads the traces flushed so far and returns the root span of
// each with its weight.
func drainRoots(tb *TraceBuffer) map[string][]rootWeight {
	out := map[string][]rootWeight{}
	for id, spans := range drain(tb) {
		for _, s := range spans {
			if s.ParentSpanID == "" {
				out[id] = append(out[id], rootWeight{s.Name, s.SampleWeight})
			}
		}
	}
	return out
}

// carryBase is a flush time half an hour into an hour, far enough ahead of
// time.Now that every buffered trace is past its TTL.
func carryBase() time.Time {
	return time.Now().Truncate(time.Hour).Add(2*time.Hour + 30*time.Minute)
}

// TestCarryQuietOperation is the low-volume route from #333: 7 requests in an
// hour at ratio 1000000. The floor stores one and the other 6 are dropped.
// With no sampled trace after them, the latest dropped trace is stored after
// carryWait as their carrier, so the operation's weights add up to 7.
func TestCarryQuietOperation(t *testing.T) {
	tb := rescueBuffer(RescueRules{MinTracesPerHour: 1})
	at := carryBase()
	for i := range 7 {
		tb.Add(rootSpan(unsampledID(i), "GET /nl/terms", 1000))
	}
	tb.Flush(at)
	first := drainRoots(tb)
	if len(first) != 1 {
		t.Fatalf("flush stored %d traces, want the floor trace", len(first))
	}

	tb.Flush(at.Add(carryWait - time.Second))
	if got := drainRoots(tb); len(got) != 0 {
		t.Fatalf("carrier stored before carryWait: %v", got)
	}

	tb.Flush(at.Add(carryWait))
	carrier := drainRoots(tb)
	if len(carrier) != 1 {
		t.Fatalf("flush after carryWait stored %d traces, want 1 carrier", len(carrier))
	}
	totals := map[string]int{}
	weightSum(totals, first)
	weightSum(totals, carrier)
	if totals["GET /nl/terms"] != 7 {
		t.Errorf("weight total = %d, want 7", totals["GET /nl/terms"])
	}
	st := tb.Stats()
	if st.CarriedDrops != 6 || st.UncarriedDrops != 0 || st.CarrierSpans != 0 {
		t.Errorf("stats = carried %d, uncarried %d, held %d; want 6, 0, 0",
			st.CarriedDrops, st.UncarriedDrops, st.CarrierSpans)
	}
	if res := tb.Close(at.Add(carryWait)); res.Kept != 0 {
		t.Errorf("Close kept %d spans, want 0 once the drops are carried", res.Kept)
	}
}

// TestCarryNextFloorTrace checks that the next hour's floor trace carries the
// drops of the hour before when it comes within carryWait, so no extra trace
// is stored for them.
func TestCarryNextFloorTrace(t *testing.T) {
	tb := rescueBuffer(RescueRules{MinTracesPerHour: 1})
	at := carryBase()
	for i := range 4 {
		tb.Add(rootSpan(unsampledID(i), "GET /security", 1000))
	}
	tb.Flush(at)
	if got := drainRoots(tb); len(got) != 1 {
		t.Fatalf("flush stored %d traces, want the floor trace", len(got))
	}

	tb.Add(rootSpan(unsampledID(10), "GET /security", 1000))
	next := at.Add(31 * time.Minute)
	tb.Flush(next)
	got := drainRoots(tb)
	r, ok := got[unsampledID(10)]
	if !ok || len(got) != 1 {
		t.Fatalf("next hour stored %v, want only its floor trace", got)
	}
	if r[0].weight != 4 {
		t.Errorf("next floor trace weight = %d, want 1 + the 3 dropped", r[0].weight)
	}
	if res := tb.Close(next); res.Kept != 0 {
		t.Errorf("Close kept %d spans, want 0", res.Kept)
	}
}

// TestCarryRatioSampledTotalsAreExact checks the overcount stays gone: at
// ratio 4 the stored weights add up to the traces seen, whichever of them
// the ratio picked.
func TestCarryRatioSampledTotalsAreExact(t *testing.T) {
	tb := weightBuffer(RescueRules{})
	at := carryBase()
	const n = 40
	for i := range n {
		tb.Add(rootSpan(fmt.Sprintf("%016x%016x", i, i), "GET /", 1000))
	}
	tb.Flush(at)
	totals := map[string]int{}
	weightSum(totals, drainRoots(tb))
	out := collect(tb)
	tb.Close(at)
	for _, s := range <-out {
		totals[s.Name] += s.SampleWeight
	}
	if totals["GET /"] != n {
		t.Errorf("weight total = %d, want %d", totals["GET /"], n)
	}
}

// TestCarrySkipsErrorAndSlowTraces checks that error and slow traces stand
// for themselves only. Their rules keep every trace of their kind, so a clean
// drop counted inside one would be read as an error or a slow request.
func TestCarrySkipsErrorAndSlowTraces(t *testing.T) {
	tb := rescueBuffer(RescueRules{SlowThresholdUs: 500})
	at := carryBase()
	tb.Add(rootSpan(unsampledID(0), "GET /x", 100))
	tb.Flush(at)
	if got := drainRoots(tb); len(got) != 0 {
		t.Fatalf("clean trace stored: %v", got)
	}

	tb.Add(rootSpan(unsampledID(1), "GET /x", 100))
	tb.Add(span(unsampledID(1), "s2", "s1", "db", "error"))
	tb.Add(rootSpan(unsampledID(2), "GET /x", 5000))
	tb.Flush(at.Add(time.Second))
	got := drainRoots(tb)
	if len(got) != 2 {
		t.Fatalf("stored %d traces, want the error and the slow trace", len(got))
	}
	for id, r := range got {
		if r[0].weight != 1 {
			t.Errorf("trace %s weight = %d, want 1", id, r[0].weight)
		}
	}

	out := collect(tb)
	if res := tb.Close(at.Add(time.Second)); res.Kept != 1 {
		t.Fatalf("Close kept %d spans, want the carrier of the clean drop", res.Kept)
	}
	if spans := <-out; len(spans) != 1 || spans[0].SampleWeight != 1 || spans[0].TraceID != unsampledID(0) {
		t.Errorf("Close delivered %+v, want the dropped clean trace with weight 1", spans)
	}
}

// TestCarryTallyHeldBudget checks the span budget of held carriers: a drop
// that does not fit keeps the trace held before it, and drops with nothing
// held are counted as uncarried.
func TestCarryTallyHeldBudget(t *testing.T) {
	c := newCarryTally(10) // 2 held spans
	at := carryBase()
	small := weightTrace("a", rootSpan("a", "GET /a", 100))
	big := weightTrace("b", rootSpan("b", "GET /a", 100), span("b", "s2", "s1", "db", "OK"), span("b", "s3", "s1", "db", "OK"))
	lone := weightTrace("c", rootSpan("c", "GET /c", 100), span("c", "s2", "s1", "db", "OK"), span("c", "s3", "s1", "db", "OK"))

	c.drop(small, at)
	c.drop(big, at)
	c.drop(lone, at)
	if held, _, _ := c.stats(); held != 1 {
		t.Fatalf("held spans = %d, want 1", held)
	}

	out := c.expired(at, true)
	if len(out) != 1 || out[0] != small || small.spans[0].SampleWeight != 2 {
		t.Fatalf("expired = %d carriers, want the small trace with weight 2", len(out))
	}
	held, carried, uncarried := c.stats()
	if held != 0 || carried != 2 || uncarried != 1 {
		t.Errorf("stats = held %d, carried %d, uncarried %d; want 0, 2, 1", held, carried, uncarried)
	}
}
