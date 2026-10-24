package alert

import (
	"context"
	"errors"
	"sync"
)

// evalFailConfirm is how many evaluations of one alert in a row must time out
// before the failure logs at Error. The runner repeats every alert on the next
// tick, so one timed-out read loses nothing; a timeout that keeps coming back
// means the alert is not being checked.
const evalFailConfirm = 3

// evalFailures counts consecutive timed-out evaluations per alert.
type evalFailures struct {
	mu     sync.Mutex
	counts map[int64]int
}

// timedOut reports whether err ended because a deadline passed or the context
// was cancelled.
func timedOut(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// record notes the outcome of one evaluation of alertID and reports whether a
// failure should log at Error. Errors other than timeouts always do.
func (f *evalFailures) record(alertID int64, err error) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil || !timedOut(err) {
		delete(f.counts, alertID)
		return err != nil
	}
	if f.counts == nil {
		f.counts = make(map[int64]int)
	}
	f.counts[alertID]++
	return f.counts[alertID] >= evalFailConfirm
}
