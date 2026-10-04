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
	name    string
	attrs   map[string]string
	fn      func() (float64, bool)
	counter bool // exported as a cumulative monotonic sum instead of a gauge
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
	if fn == nil {
		return
	}
	r.register(gaugeSrc{name: name, attrs: attrs, fn: always(fn)})
}

// RegisterOptionalGauge is RegisterGauge for a value that is not always known.
// When fn reports false the snapshot leaves the point out, so a reading that
// has not been taken yet (disk usage before the first retention cycle, say) is
// absent rather than a zero that looks real.
func (r *Recorder) RegisterOptionalGauge(name string, attrs map[string]string, fn func() (float64, bool)) {
	if fn == nil {
		return
	}
	r.register(gaugeSrc{name: name, attrs: attrs, fn: fn})
}

// RegisterCounter adds a cumulative, monotonic counter read on each snapshot,
// for totals another component already keeps (rows retention has deleted).
func (r *Recorder) RegisterCounter(name string, attrs map[string]string, fn func() float64) {
	if fn == nil {
		return
	}
	r.register(gaugeSrc{name: name, attrs: attrs, fn: always(fn), counter: true})
}

func (r *Recorder) register(g gaugeSrc) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.gauges = append(r.gauges, g)
	r.mu.Unlock()
}

// Reading is one registered gauge or counter value at a point in time.
type Reading struct {
	Name  string
	Attrs map[string]string
	Value float64
}

// Readings samples every registered gauge and counter, leaving out optional
// gauges that have no value yet. It does not touch the request counters or
// the latency histogram, so calling it does not disturb the next export.
func (r *Recorder) Readings() []Reading {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	gauges := make([]gaugeSrc, len(r.gauges))
	copy(gauges, r.gauges)
	r.mu.Unlock()

	sampled := sample(gauges)
	out := make([]Reading, 0, len(sampled))
	for _, g := range sampled {
		out = append(out, Reading{Name: g.name, Attrs: g.attrs, Value: g.value})
	}
	return out
}

// sample reads each source, skipping optional gauges with no value. Call it
// without the recorder lock: a source may do I/O.
func sample(gauges []gaugeSrc) []gaugeReading {
	var out []gaugeReading
	for _, g := range gauges {
		if v, ok := g.fn(); ok {
			out = append(out, gaugeReading{name: g.name, attrs: g.attrs, value: v, counter: g.counter})
		}
	}
	return out
}

func always(fn func() float64) func() (float64, bool) {
	return func() (float64, bool) { return fn(), true }
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
	name    string
	attrs   map[string]string
	value   float64
	counter bool
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

	snap.gauges = sample(gauges)
	return snap
}
