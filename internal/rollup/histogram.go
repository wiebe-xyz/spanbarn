package rollup

import (
	"encoding/json"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// histogram is an explicit-bucket histogram as stored in a rollup's extra
// column: upper bounds and the count in each bucket.
type histogram struct {
	Bounds []float64 `json:"bounds"`
	Counts []float64 `json:"counts"`
}

// histogramOf parses a row's extra column, returning nil when the row carries no
// explicit-bucket histogram.
func histogramOf(m repository.MetricRollup) *histogram {
	if m.Type != "histogram" || m.Extra == "" {
		return nil
	}
	var h histogram
	if json.Unmarshal([]byte(m.Extra), &h) != nil || len(h.Counts) == 0 {
		return nil
	}
	return &h
}

// clone returns a deep copy, so differencing never mutates a caller's snapshot.
func (h *histogram) clone() *histogram {
	if h == nil {
		return nil
	}
	return &histogram{
		Bounds: append([]float64(nil), h.Bounds...),
		Counts: append([]float64(nil), h.Counts...),
	}
}

// sub returns the observations made since prev, for a cumulative histogram whose
// counts only ever grow. A shrinking bucket means the process restarted and the
// snapshot is already the count since that restart, so it is taken whole. With
// no previous snapshot the answer is unknowable and nothing is contributed —
// the same choice the counter path makes, and for the same reason: guessing
// would inflate every sparse series.
func (h *histogram) sub(prev *histogram) *histogram {
	if h == nil {
		return nil
	}
	if prev == nil || len(prev.Counts) != len(h.Counts) {
		return nil
	}
	out := h.clone()
	for i := range out.Counts {
		d := h.Counts[i] - prev.Counts[i]
		if d < 0 {
			return h.clone() // reset: the snapshot is the delta
		}
		out.Counts[i] = d
	}
	return out
}

// total is the number of observations the histogram holds.
func (h *histogram) total() float64 {
	if h == nil {
		return 0
	}
	var n float64
	for _, c := range h.Counts {
		n += c
	}
	return n
}

// marshal renders the histogram for the extra column.
func (h *histogram) marshal() string {
	if h == nil || len(h.Counts) == 0 {
		return ""
	}
	b, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return string(b)
}

// addHistogram adds src into dst elementwise. Bucket bounds are stable per
// series in practice; when two series disagree the larger sample wins, because
// silently adding mismatched bounds would report a distribution that never
// happened. Returns the merged histogram.
func addHistogram(dst, src *histogram) *histogram {
	if src == nil {
		return dst
	}
	if dst == nil {
		return src.clone()
	}
	if len(dst.Counts) != len(src.Counts) {
		if src.total() > dst.total() {
			return src.clone()
		}
		return dst
	}
	for i := range src.Counts {
		dst.Counts[i] += src.Counts[i]
	}
	return dst
}
