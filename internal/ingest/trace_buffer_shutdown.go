package ingest

import "time"

// CloseResult says what Close did with the traces still in the buffer.
type CloseResult struct {
	Traces int // traces decided
	Spans  int // spans in those traces
	Kept   int // spans sent to Out
}

// Close decides every trace the buffer still holds, whatever its age, sends
// the kept ones to Out and then closes Out. The buffer lives in memory and
// holds each trace for the whole TTL, so without this a process that exits
// loses every trace still waiting for its decision: about ten minutes of
// traffic, error traces included, on every rollout.
//
// Call it once nothing calls Add any more. Out must still be drained while
// Close runs. Later calls do nothing and return a zero result.
func (tb *TraceBuffer) Close(now time.Time) CloseResult {
	var res CloseResult
	tb.closeOnce.Do(func() {
		close(tb.stop)
		<-tb.gcDone

		tb.mu.Lock()
		res.Traces, res.Spans = len(tb.traces), tb.spanCount
		tb.mu.Unlock()

		res.Kept = tb.flush(now, true)
		close(tb.out)
	})
	return res
}
