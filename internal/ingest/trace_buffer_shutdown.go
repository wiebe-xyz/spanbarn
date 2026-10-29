package ingest

import "time"

// drainTick is how often Drain decides the traces that came due. It is the gc
// loop's interval, so a draining pod releases on the same cadence as a
// running one and a rollout looks like steady state on a chart.
const drainTick = gcInterval

// CloseResult says what Close did with the traces still in the buffer.
type CloseResult struct {
	Traces int // traces decided
	Spans  int // spans in those traces
	Kept   int // spans sent to Out
}

// Close decides every trace the buffer still holds, whatever its age, sends
// the kept ones to Out and then closes Out. The buffer lives in memory, so
// without this a process that exits loses every trace still waiting for its
// decision, error traces included, on every rollout.
//
// Call it once nothing calls Add any more. Out must still be drained while
// Close runs. Later calls, and calls after Drain, do nothing and return a
// zero result.
func (tb *TraceBuffer) Close(now time.Time) CloseResult {
	var res CloseResult
	tb.closeOnce.Do(func() {
		res = tb.stopGC()
		res.Kept = tb.finish(now)
	})
	return res
}

// Drain is Close for a pod that hands its traffic to the next one. It decides
// the traces in the order they come due, until no trace whose root span
// arrived is left or deadline passes, and then decides the rest at once the
// way Close does. A rollout then spreads the old pod's buffer over the root
// grace period while the new pod's buffer fills, so the stored spans per
// minute stay flat instead of showing a burst and a hole (#451). Traces
// without a root would wait the whole TTL, so they go at the end.
func (tb *TraceBuffer) Drain(deadline time.Time) CloseResult {
	var res CloseResult
	tb.closeOnce.Do(func() {
		res = tb.stopGC()
		tick := drainTick
		if g := tb.RootGrace(); g > 0 && g < tick {
			tick = g
		}
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			now := tb.now()
			sent, rooted := tb.drainStep(now)
			res.Kept += sent
			if rooted == 0 || !now.Before(deadline) {
				break
			}
			<-ticker.C
		}
		res.Kept += tb.finish(tb.now())
	})
	return res
}

// drainStep decides the traces that came due and returns the spans it sent
// and how many traces with a root span are left.
func (tb *TraceBuffer) drainStep(now time.Time) (sent, rooted int) {
	sent = tb.flush(now, false)
	tb.mu.Lock()
	defer tb.mu.Unlock()
	for _, tr := range tb.traces {
		if tr.hasRoot() {
			rooted++
		}
	}
	return sent, rooted
}

// stopGC ends the gc loop and returns what the buffer holds.
func (tb *TraceBuffer) stopGC() CloseResult {
	close(tb.stop)
	<-tb.gcDone
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return CloseResult{Traces: len(tb.traces), Spans: tb.spanCount}
}

// finish decides every trace left, closes Out and returns the spans sent.
func (tb *TraceBuffer) finish(now time.Time) int {
	sent := tb.flush(now, true)
	close(tb.out)
	return sent
}
