package ingest

import "time"

// The buffer at its cap sheds unsampled traces, and one of those can still
// turn into an error trace after it was shed. A short stretch at the cap is
// a burst and logs at info. A long one means the cap is too small for the
// traffic, and is warned about so it reaches BugBarn: before root release,
// prod sat at its cap for weeks and only an info line said so.
const (
	// capPressureWarnAfter is how long the buffer stays at its cap before it
	// warns.
	capPressureWarnAfter = 10 * time.Minute
	// capPressureRepeat is the least time between two warnings.
	capPressureRepeat = time.Hour
	// capPressureFillPct is the fill, in percent of the cap, at or above which
	// a flush window with no eviction still counts as at the cap.
	capPressureFillPct = 90
)

// capPressure tracks how long the buffer has been at its cap.
type capPressure struct {
	since  time.Time // start of the current stretch at the cap; zero below it
	warned time.Time // time of the last warning
}

// observe records one flush window. It returns how long the buffer has been
// at its cap when that calls for a warning, and 0 otherwise.
func (p *capPressure) observe(w lossWindow, now time.Time) time.Duration {
	if !w.atCap() {
		p.since = time.Time{}
		return 0
	}
	if p.since.IsZero() {
		p.since = now
	}
	held := now.Sub(p.since)
	if held < capPressureWarnAfter || (!p.warned.IsZero() && now.Sub(p.warned) < capPressureRepeat) {
		return 0
	}
	p.warned = now
	return held
}

// atCap reports whether the cap acted in the window, or the buffer is still
// close to it.
func (w lossWindow) atCap() bool {
	if w.evictedSacrif > 0 || w.rescued > 0 || w.evictedKept > 0 || w.refused > 0 {
		return true
	}
	return w.maxSpans > 0 && w.buffered*100 >= w.maxSpans*capPressureFillPct
}

// reportSustainedCap warns when the buffer has been at its cap long enough.
func (tb *TraceBuffer) reportSustainedCap(w lossWindow) {
	if w.atCapFor <= 0 {
		return
	}
	tb.logger.Warn("trace buffer at capacity for a sustained stretch",
		"minutes", int(w.atCapFor/time.Minute),
		"max_spans", w.maxSpans,
		"buffered_spans", w.buffered,
		"buffered_traces", w.traces,
		"hint", "raise SPANBARN_TRACE_BUFFER_MAX_SPANS; unsampled traces shed at the cap can no longer be kept for a late error")
}
