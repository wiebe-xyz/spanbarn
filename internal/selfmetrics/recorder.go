// Package selfmetrics collects a handful of in-process signals (request rate,
// request latency, spool size, redis-queue depth, rollups persisted) and exports
// them as OTLP metrics to SpanBarn's own ingest endpoint — the metrics analogue
// of the existing self-tracing and self-logging. This makes SpanBarn dogfood its
// own metrics product so the Metrics page always has live data.
package selfmetrics

import "sync"

// defaultDurationBoundsMs are the explicit histogram boundaries (milliseconds)
// for request latency. Chosen to give useful p50/p95/p99 resolution from
// sub-millisecond up to multi-second responses.
var defaultDurationBoundsMs = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// Recorder accumulates self-metrics. All methods are safe on a nil Recorder, so
// callers can hold an optional *Recorder without nil checks. Counters are
// cumulative; the latency histogram is reset each snapshot (delta temporality).
type Recorder struct {
	mu          sync.Mutex
	reqByStatus map[string]int64 // status class ("2xx") -> cumulative count
	durBounds   []float64
	durCounts   []int64 // len(durBounds)+1, reset each snapshot
	durSum      float64 // reset each snapshot
	durCount    int64   // reset each snapshot
	rollups     int64   // cumulative metric rollups persisted
	gauges      []gaugeSrc
}

type gaugeSrc struct {
	name  string
	attrs map[string]string
	fn    func() float64
}

// NewRecorder returns a ready Recorder.
func NewRecorder() *Recorder {
	return &Recorder{
		reqByStatus: map[string]int64{},
		durBounds:   defaultDurationBoundsMs,
		durCounts:   make([]int64, len(defaultDurationBoundsMs)+1),
	}
}

// RecordRequest counts one HTTP request in its status class and bins its latency.
func (r *Recorder) RecordRequest(statusClass string, durationMs float64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqByStatus[statusClass]++
	idx := len(r.durBounds) // overflow bucket
	for i, b := range r.durBounds {
		if durationMs <= b {
			idx = i
			break
		}
	}
	r.durCounts[idx]++
	r.durSum += durationMs
	r.durCount++
}

// AddRollups records that n metric rollups were persisted.
func (r *Recorder) AddRollups(n int64) {
	if r == nil || n <= 0 {
		return
	}
	r.mu.Lock()
	r.rollups += n
	r.mu.Unlock()
}

// RegisterGauge adds a sampled gauge read on each snapshot. fn is invoked
// without the recorder lock held, so it may safely do I/O (e.g. a redis LLEN).
func (r *Recorder) RegisterGauge(name string, attrs map[string]string, fn func() float64) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	r.gauges = append(r.gauges, gaugeSrc{name: name, attrs: attrs, fn: fn})
	r.mu.Unlock()
}

// snapshot captures the current values, resetting the delta histogram. Gauge
// providers are invoked after the lock is released.
type snapshot struct {
	requests  map[string]int64
	durBounds []float64
	durCounts []int64
	durSum    float64
	durCount  int64
	rollups   int64
	gauges    []gaugeReading
}

type gaugeReading struct {
	name  string
	attrs map[string]string
	value float64
}

func (r *Recorder) snapshot() snapshot {
	if r == nil {
		return snapshot{}
	}
	r.mu.Lock()
	reqs := make(map[string]int64, len(r.reqByStatus))
	for k, v := range r.reqByStatus {
		reqs[k] = v
	}
	counts := make([]int64, len(r.durCounts))
	copy(counts, r.durCounts)
	snap := snapshot{
		requests:  reqs,
		durBounds: r.durBounds,
		durCounts: counts,
		durSum:    r.durSum,
		durCount:  r.durCount,
		rollups:   r.rollups,
	}
	// Reset the delta histogram for the next interval.
	for i := range r.durCounts {
		r.durCounts[i] = 0
	}
	r.durSum = 0
	r.durCount = 0
	gauges := make([]gaugeSrc, len(r.gauges))
	copy(gauges, r.gauges)
	r.mu.Unlock()

	for _, g := range gauges {
		snap.gauges = append(snap.gauges, gaugeReading{name: g.name, attrs: g.attrs, value: g.fn()})
	}
	return snap
}
