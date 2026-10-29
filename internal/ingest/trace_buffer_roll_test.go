package ingest

import (
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// A rollout, simulated second by second (#451). Pod A takes the traffic until
// rollAt, then gets SIGTERM and drains in order while pod B takes the traffic.
// Each second rollTraces traces start: a child span arrives at once, the root
// one second later, and every 20th trace has a child that arrives 45 s later,
// after the decision. So the traces that start just before the roll have
// their child on A and their root on B, and late children of A's traces
// arrive at B.
const (
	rollTraces = 20
	rollAt     = 150
	rollEnd    = 400
	rollLate   = 45
)

type rollSpan struct {
	rec model.SpanRecord
	at  int // second it is stored at
}

type rollResult struct {
	stored []rollSpan
	roots  int             // root spans sent
	traces map[string]int  // spans sent per trace
	errors map[string]bool // traces sent with an error span
}

// rollTraceID spreads the trace IDs over the hash space.
func rollTraceID(n int) string {
	return fmt.Sprintf("%016x%016x", uint64(n)*0x9E3779B97F4A7C15, n)
}

// rollTraffic returns the spans that arrive in each second.
func rollTraffic() map[int][]model.SpanRecord {
	at := map[int][]model.SpanRecord{}
	for s := range rollEnd {
		for i := range rollTraces {
			n := s*rollTraces + i
			id := rollTraceID(n)
			status := "OK"
			if n%25 == 0 {
				status = "ERROR"
			}
			at[s] = append(at[s], childSpan(id, "c", status))
			at[s+1] = append(at[s+1], rootOf(id))
			if n%20 == 0 {
				at[s+rollLate] = append(at[s+rollLate], childSpan(id, "late", "OK"))
			}
		}
	}
	return at
}

// simulateRoll runs the rollout. With ordered set, A drains in order and both
// pods release a trace 30 s after its root; without it, A decides everything
// at SIGTERM and B waits the TTL, the behaviour before #451.
func simulateRoll(t *testing.T, ratio int, ordered bool) rollResult {
	t.Helper()
	clock := newSimClock()
	start := clock.now()
	pod := func() *TraceBuffer {
		tb := NewTraceBufferWithRules(10*time.Minute, 0, RescueRules{}, NewStaticRatioLookup(ratio), testLogger())
		tb.now = clock.now
		// Room for everything a pod sends in one step, so takeOut after each
		// step sees it all and deliver never waits for a reader.
		out := make(chan []model.SpanRecord, 1<<16)
		tb.out, tb.Out = out, out
		if !ordered {
			tb.SetRootGrace(0)
		}
		return tb
	}
	a, b := pod(), pod()
	res := rollResult{traces: map[string]int{}, errors: map[string]bool{}}
	collect := func(tb *TraceBuffer, s int) {
		for _, rec := range takeOut(tb) {
			res.stored = append(res.stored, rollSpan{rec: rec, at: s})
		}
	}
	traffic := rollTraffic()
	aOpen, aDraining := true, false
	for s := 0; s <= rollEnd+31; s++ {
		now := start.Add(time.Duration(s) * time.Second)
		clock.set(now)
		for _, rec := range traffic[s] {
			if s >= rollEnd {
				break
			}
			res.traces[rec.TraceID] = res.traces[rec.TraceID] + 1
			if rec.ParentSpanID == "" {
				res.roots++
			}
			if rec.Status == "ERROR" {
				res.errors[rec.TraceID] = true
			}
			if s < rollAt {
				a.Add(rec)
			} else {
				b.Add(rec)
			}
		}
		switch {
		case s == rollAt && !ordered:
			a.Close(now)
			aOpen = false
		case s == rollAt:
			a.closeOnce.Do(func() { a.stopGC() })
			aDraining = true
		}
		if aDraining && s%5 == 0 {
			if _, rooted := a.drainStep(now); rooted == 0 {
				a.finish(now)
				aDraining, aOpen = false, false
			}
		} else if aOpen && s%5 == 0 {
			a.Flush(now)
		}
		collect(a, s)
		if s%5 == 0 {
			b.Flush(now)
		}
		collect(b, s)
	}
	end := start.Add(time.Duration(rollEnd+31) * time.Second)
	b.Close(end)
	collect(b, rollEnd+31)
	return res
}

// perMinute counts the stored spans per minute from 60 s on, when the first
// traces have been decided, up to the minute traffic stops in.
func perMinute(stored []rollSpan) []int {
	counts := make([]int, (rollEnd-60)/60)
	for _, s := range stored {
		if m := (s.at - 60) / 60; s.at >= 60 && m < len(counts) {
			counts[m]++
		}
	}
	return counts
}

// TestRolloutKeepsSpansPerMinuteFlat: with ratio 1 every span is stored, so
// each minute stores the same number of spans as traffic brings in, rollout
// minutes included. They differ by the few spans whose trace is split over
// both pods. Late spans count too: without the decided map they wait the TTL
// and every minute stores 2.4% too few. The old behaviour fails this check:
// B stores nothing for the TTL after the roll.
func TestRolloutKeepsSpansPerMinuteFlat(t *testing.T) {
	const steady = rollTraces*60*2 + rollTraces*60/20 // child, root and late spans per minute
	counts := perMinute(simulateRoll(t, 1, true).stored)
	for m, n := range counts {
		if n < steady*98/100 || n > steady*102/100 {
			t.Errorf("minute %d stored %d spans, want %d ±2%% (all minutes: %v)", m+1, n, steady, counts)
		}
	}

	old := perMinute(simulateRoll(t, 1, false).stored)
	low := steady
	for _, n := range old {
		low = min(low, n)
	}
	if low > steady/2 {
		t.Errorf("the check does not catch the old behaviour: minutes %v", old)
	}
}

// TestRolloutDecidesEachTraceOnce: with ratio 4, both pods take the same
// keep or drop for every trace, so a clean trace is stored whole or not at
// all, and the root weights add up to every trace sent, each counted once.
func TestRolloutDecidesEachTraceOnce(t *testing.T) {
	res := simulateRoll(t, 4, true)
	stored := map[string]int{}
	errored := map[string]bool{}
	weights := 0
	for _, s := range res.stored {
		stored[s.rec.TraceID]++
		if s.rec.Status == "ERROR" {
			errored[s.rec.TraceID] = true
		}
		if s.rec.ParentSpanID == "" {
			weights += s.rec.SampleWeight
		}
	}
	split := 0
	for id, sent := range res.traces {
		if res.errors[id] {
			if !errored[id] {
				t.Errorf("error trace %s: error span not stored", id)
			}
			continue
		}
		if n := stored[id]; n != 0 && n != sent {
			split++
			t.Errorf("trace %s: stored %d of %d spans", id, n, sent)
		}
	}
	if split > 0 {
		t.Fatalf("%d clean traces stored in part", split)
	}
	if weights != res.roots {
		t.Errorf("root weights add up to %d, want %d traces", weights, res.roots)
	}
}
