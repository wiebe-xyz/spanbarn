package ingest

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// simClock is a settable clock for the buffer's arrival times. Flush and
// Drain get the same times passed in. It starts at the real time, so the
// buffer's own gc loop, which flushes at the real time, never finds a trace
// due while a test runs.
type simClock struct{ at time.Time }

func newSimClock() *simClock                        { return &simClock{at: time.Now()} }
func (c *simClock) now() time.Time                  { return c.at }
func (c *simClock) after(d time.Duration) time.Time { return c.at.Add(d) }
func (c *simClock) set(t time.Time)                 { c.at = t }

func releaseBuffer(t *testing.T, ratio int) (*TraceBuffer, *simClock) {
	t.Helper()
	clock := newSimClock()
	tb := NewTraceBufferWithRules(time.Hour, 0, RescueRules{}, NewStaticRatioLookup(ratio), testLogger())
	tb.now = clock.now
	return tb, clock
}

// takeOut returns what the buffer has sent to Out so far, without blocking.
func takeOut(tb *TraceBuffer) []model.SpanRecord {
	var out []model.SpanRecord
	for {
		select {
		case spans, ok := <-tb.Out:
			if !ok {
				return out
			}
			out = append(out, spans...)
		default:
			return out
		}
	}
}

func childSpan(traceID, spanID, status string) model.SpanRecord {
	return model.SpanRecord{ProjectID: 1, TraceID: traceID, SpanID: spanID, ParentSpanID: "r", Name: "db.query", Status: status}
}

func rootOf(traceID string) model.SpanRecord {
	return model.SpanRecord{ProjectID: 1, TraceID: traceID, SpanID: "r", Name: "GET /", Status: "OK", DurationUs: 1000}
}

// A trace is decided once its root span has been in for the grace period,
// long before the TTL. A trace whose root never came still waits the TTL.
func TestRootedTraceIsDecidedAfterTheGrace(t *testing.T) {
	tb, clock := releaseBuffer(t, 1)
	defer tb.Close(clock.now())
	start := clock.now()

	tb.Add(childSpan("rooted", "c", "OK"))
	tb.Add(childSpan("rootless", "c", "OK"))
	clock.set(start.Add(time.Second))
	tb.Add(rootOf("rooted"))

	tb.Flush(start.Add(DefaultRootGrace))
	if got := takeOut(tb); len(got) != 0 {
		t.Fatalf("flush before the grace ran out sent %d spans", len(got))
	}
	tb.Flush(start.Add(time.Second + DefaultRootGrace))
	if got := takeOut(tb); len(got) != 2 || got[0].TraceID != "rooted" {
		t.Fatalf("flush at the grace sent %v, want the rooted trace's 2 spans", got)
	}
	tb.Flush(start.Add(time.Hour - time.Second))
	if got := takeOut(tb); len(got) != 0 {
		t.Fatalf("rootless trace sent before the TTL: %v", got)
	}
	tb.Flush(start.Add(time.Hour))
	if got := takeOut(tb); len(got) != 1 || got[0].TraceID != "rootless" {
		t.Fatalf("flush at the TTL sent %v, want the rootless trace", got)
	}
}

func TestRootGraceZeroWaitsTheTTL(t *testing.T) {
	tb, clock := releaseBuffer(t, 1)
	defer tb.Close(clock.now())
	tb.SetRootGrace(0)
	tb.Add(rootOf("t1"))
	tb.Flush(clock.after(time.Hour - time.Second))
	if got := takeOut(tb); len(got) != 0 {
		t.Fatalf("grace 0 sent the trace before the TTL: %v", got)
	}
}

// A span that arrives after its trace was decided follows the decision: it is
// stored with the trace's weight when the trace was kept, and dropped when it
// was not. An error span keeps itself and the rest of the trace.
func TestLateSpanFollowsTheDecision(t *testing.T) {
	const ratio = 4
	kept, dropped := weightTraceID(t, true, 0), weightTraceID(t, false, 0)
	tb, clock := releaseBuffer(t, ratio)
	defer tb.Close(clock.now())
	start := clock.now()

	tb.Add(rootOf(dropped)) // dropped first, so the kept trace carries it
	tb.Add(rootOf(kept))
	tb.Flush(start.Add(DefaultRootGrace))
	first := takeOut(tb)
	if len(first) != 1 || first[0].TraceID != kept || first[0].SampleWeight != 2 {
		t.Fatalf("decision sent %+v, want the kept root with weight 2", first)
	}

	clock.set(start.Add(DefaultRootGrace + time.Second))
	tb.Add(childSpan(kept, "late", "OK"))
	tb.Add(childSpan(dropped, "late", "OK"))
	tb.Add(childSpan(dropped, "late-err", "ERROR"))
	tb.Add(childSpan(dropped, "after-err", "OK"))
	if st := tb.Stats(); st.BufferedTraces != 0 {
		t.Fatalf("late spans started %d new traces, want 0", st.BufferedTraces)
	}

	tb.Flush(start.Add(DefaultRootGrace + 5*time.Second))
	got := map[string]int{}
	for _, s := range takeOut(tb) {
		got[s.TraceID+"/"+s.SpanID] = s.SampleWeight
	}
	want := map[string]int{kept + "/late": 2, dropped + "/late-err": 1, dropped + "/after-err": 1}
	if len(got) != len(want) {
		t.Fatalf("late spans sent %v, want %v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: weight %d, want %d", k, got[k], w)
		}
	}
	if st := tb.Stats(); st.LateSpansKept != 3 || st.LateSpansDropped != 1 {
		t.Errorf("late spans kept %d, dropped %d; want 3, 1", st.LateSpansKept, st.LateSpansDropped)
	}
}

// A decision is held for the TTL. A span after that is buffered again.
func TestDecisionExpiresAfterTheTTL(t *testing.T) {
	tb, clock := releaseBuffer(t, 1)
	defer tb.Close(clock.now())
	start := clock.now()
	tb.Add(rootOf("t1"))
	tb.Flush(start.Add(DefaultRootGrace))
	takeOut(tb)

	tb.Flush(start.Add(DefaultRootGrace + time.Hour - time.Second))
	if st := tb.Stats(); st.DecidedTraces != 1 {
		t.Fatalf("decisions held before the TTL = %d, want 1", st.DecidedTraces)
	}
	tb.Flush(start.Add(DefaultRootGrace + time.Hour))
	if st := tb.Stats(); st.DecidedTraces != 0 {
		t.Fatalf("decisions held after the TTL = %d, want 0", st.DecidedTraces)
	}
	tb.Add(childSpan("t1", "c", "OK"))
	if st := tb.Stats(); st.BufferedTraces != 1 {
		t.Fatalf("span after the TTL buffered %d traces, want 1", st.BufferedTraces)
	}
}

// At the cap the oldest decision goes first, and the eviction is counted.
func TestDecisionCapEvictsTheOldest(t *testing.T) {
	d := newDecidedTraces(time.Hour, 2)
	at := time.Now()
	d.put("a", 1, false, at)
	d.put("b", 0, false, at)
	d.put("a", 3, false, at) // an update takes no room
	d.put("c", 1, false, at)
	if _, ok := d.m["a"]; ok || len(d.m) != 2 || d.evicted != 1 {
		t.Fatalf("held %v, evicted %d; want b and c, 1 evicted", d.m, d.evicted)
	}
	d.expire(at.Add(time.Hour))
	if len(d.m) != 0 || d.evicted != 1 {
		t.Fatalf("expiry left %d decisions, evicted %d; want 0, 1", len(d.m), d.evicted)
	}
}

// A span that arrives while its trace is decided outside the lock waits, and
// then follows the decision.
func TestSpanWhileDecidingWaitsForTheDecision(t *testing.T) {
	d := newDecidedTraces(time.Hour, 0)
	at := time.Now()
	d.put("t1", 0, true, at)
	if !d.follow(childSpan("t1", "c", "OK")) || len(d.late) != 0 {
		t.Fatal("span of a pending trace was not held")
	}
	held := d.put("t1", 5, false, at)
	if len(held) != 1 {
		t.Fatalf("decision returned %d held spans, want 1", len(held))
	}
	d.follow(held[0])
	if late := d.takeLate(); len(late) != 1 || late[0].SampleWeight != 5 {
		t.Fatalf("held span sent as %+v, want weight 5", late)
	}
}

// A trace whose root this pod never saw is not counted as a drop: the pod
// that holds the root counts it. Kept by the ratio, it stands for the ratio.
func TestRootlessTraceIsNotCarried(t *testing.T) {
	const ratio = 4
	kept, dropped := weightTraceID(t, true, 0), weightTraceID(t, false, 0)
	tb, clock := releaseBuffer(t, ratio)
	defer tb.Close(clock.now())
	tb.Add(childSpan(dropped, "c", "OK"))
	tb.Add(childSpan(kept, "c", "OK"))
	tb.Flush(clock.after(time.Hour))
	got := takeOut(tb)
	if len(got) != 1 || got[0].TraceID != kept || got[0].SampleWeight != ratio {
		t.Fatalf("sent %+v, want the sampled part with weight %d", got, ratio)
	}
	if st := tb.Stats(); st.CarriedDrops != 0 || st.CarrierSpans != 0 {
		t.Errorf("rootless drop was carried: %+v", st)
	}
}

// Drain sends the rooted traces as they come due and the rootless rest at the
// end, then closes Out.
func TestDrainSendsRootedTracesFirst(t *testing.T) {
	tb := NewTraceBufferWithRules(time.Hour, 0, RescueRules{}, NewStaticRatioLookup(1), testLogger())
	tb.SetRootGrace(20 * time.Millisecond)
	tb.Add(childSpan("rootless", "c", "OK"))
	tb.Add(rootOf("rooted"))

	var order []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for spans := range tb.Out {
			order = append(order, spans[0].TraceID)
		}
	}()
	start := time.Now()
	res := tb.Drain(start.Add(time.Minute))
	<-done
	if res.Kept != 2 || len(order) != 2 || order[0] != "rooted" {
		t.Fatalf("drain kept %d spans in order %v, want rooted then rootless", res.Kept, order)
	}
	if waited := time.Since(start); waited < 20*time.Millisecond || waited > 10*time.Second {
		t.Errorf("drain took %s, want about the root grace", waited)
	}
	if again := tb.Close(time.Now()); again != (CloseResult{}) {
		t.Errorf("Close after Drain = %+v, want a no-op", again)
	}
}

// A deadline that has passed decides everything at once, like Close.
func TestDrainPastDeadlineDecidesAtOnce(t *testing.T) {
	tb := NewTraceBufferWithRules(time.Hour, 0, RescueRules{}, NewStaticRatioLookup(1), testLogger())
	tb.Add(rootOf("rooted"))
	done := make(chan int)
	go func() {
		n := 0
		for spans := range tb.Out {
			n += len(spans)
		}
		done <- n
	}()
	if res := tb.Drain(time.Now().Add(-time.Second)); res.Kept != 1 || <-done != 1 {
		t.Fatalf("drain past its deadline kept %d spans, want 1", res.Kept)
	}
}

// Due traces are decided, and sent, in the order they arrived, so the trace
// that carries a dropped one is the next kept trace and never a matter of map
// order.
func TestDueTracesAreDecidedInArrivalOrder(t *testing.T) {
	tb, clock := releaseBuffer(t, 1)
	defer tb.Close(clock.now())
	var want []string
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("t%02d", i)
		want = append(want, id)
		tb.Add(rootOf(id))
	}
	tb.Flush(clock.after(DefaultRootGrace))
	var got []string
	for _, s := range takeOut(tb) {
		got = append(got, s.TraceID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sent %v, want arrival order %v", got, want)
	}
}
