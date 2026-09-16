package aggregation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// MetricRollupWriter persists downsampled metric buckets.
type MetricRollupWriter interface {
	UpsertMetricRollups(rollups []repository.MetricRollup) error
}

// MetricAccumulator folds raw OTLP metric data points into per-series, per-bucket
// rollups in memory, then flushes closed buckets to SQLite. It mirrors the span
// Accumulator but is type-aware: it merges histogram bucket counts, tracks the
// last cumulative value for counters, and keeps min/max/sum for gauges.
//
// Unlike the span accumulator it only flushes buckets that are already closed
// (bucket end <= now), so each bucket is normally written exactly once and the
// merged histogram distribution stored in `extra` is complete.
type MetricAccumulator struct {
	mu            sync.Mutex
	slots         map[metricKey]*metricSlot
	interval      time.Duration
	flushInterval time.Duration
	repo          MetricRollupWriter
	logger        *slog.Logger
	now           func() time.Time
	onPersist     func(int64) // optional: notified of rollups persisted (self-metrics)
}

// SetOnPersist registers a callback invoked with the number of rollups written
// on each successful flush. Used to feed self-metrics. Call before Run.
func (a *MetricAccumulator) SetOnPersist(fn func(int64)) {
	a.onPersist = fn
}

type metricKey struct {
	projectID   int64
	name        string
	fingerprint string
	bucket      time.Time
}

type metricSlot struct {
	metricType  string
	unit        string
	attributes  string
	temporality string

	count    int64
	sum      float64
	min      float64
	max      float64
	last     float64
	lastNano uint64
	obsCount int64
	haveVal  bool

	// explicit-bucket histogram merge state
	histBounds []float64
	histCounts []float64
	haveHist   bool

	// exp_histogram / summary: keep the latest data point's extra
	lastExtra json.RawMessage
}

// NewMetricAccumulator creates a MetricAccumulator. interval is the bucket
// granularity; flushInterval is how often closed buckets are written.
func NewMetricAccumulator(repo MetricRollupWriter, interval, flushInterval time.Duration, logger *slog.Logger) *MetricAccumulator {
	if logger == nil {
		logger = slog.Default()
	}
	return &MetricAccumulator{
		slots:         make(map[metricKey]*metricSlot),
		interval:      interval,
		flushInterval: flushInterval,
		repo:          repo,
		logger:        logger,
		now:           time.Now,
	}
}

// AddMetric folds a single metric data point into its bucket slot. Thread-safe.
func (a *MetricAccumulator) AddMetric(rec model.MetricRecord) {
	attrs := metrics.ParseAttributes(rec.Attributes)
	bucket := TruncateToBucket(time.Unix(0, int64(rec.TimeUnixNano)).UTC(), a.interval)
	k := metricKey{
		projectID:   rec.ProjectID,
		name:        rec.Name,
		fingerprint: metrics.Fingerprint(attrs),
		bucket:      bucket,
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	sl := a.slots[k]
	if sl == nil {
		sl = &metricSlot{
			metricType:  string(rec.Type),
			unit:        rec.Unit,
			attributes:  metrics.CanonicalAttributes(attrs),
			temporality: rec.Temporality,
		}
		a.slots[k] = sl
	}
	sl.count++

	switch rec.Type {
	case model.MetricTypeGauge, model.MetricTypeSum:
		v := rec.Value
		sl.sum += v
		if !sl.haveVal || v < sl.min {
			sl.min = v
		}
		if !sl.haveVal || v > sl.max {
			sl.max = v
		}
		sl.haveVal = true
		if rec.TimeUnixNano >= sl.lastNano {
			sl.last = v
			sl.lastNano = rec.TimeUnixNano
		}
	case model.MetricTypeHistogram:
		a.addHistogramPoint(sl, rec)
	case model.MetricTypeExponentialHistogram, model.MetricTypeSummary:
		sl.sum += rec.Value
		sl.obsCount += int64(rec.Count)
		if rec.TimeUnixNano >= sl.lastNano {
			sl.lastExtra = rec.Extra
			sl.lastNano = rec.TimeUnixNano
		}
	}
}

// addHistogramPoint folds one histogram data point into its slot.
//
// A delta point holds the observations since the last export, so points add up.
// A cumulative point holds a running snapshot since process start, so adding
// them multiplies the distribution by the number of exports that landed in the
// bucket: with a 10s export interval a 5-minute bucket claimed 30 times the
// observations it saw, and both the bucket counts and obs_count were inflated
// the same way. The newest snapshot stands for the bucket instead, which is also
// what lets compaction difference consecutive buckets into real per-window
// distributions.
func (a *MetricAccumulator) addHistogramPoint(sl *metricSlot, rec model.MetricRecord) {
	if rec.Temporality == temporalityDelta {
		sl.sum += rec.Value
		sl.obsCount += int64(rec.Count)
		a.foldHistogram(sl, rec.Extra)
		return
	}
	if sl.haveHist && rec.TimeUnixNano < sl.lastNano {
		return // an older snapshot than the one already held
	}
	sl.sum = rec.Value
	sl.obsCount = int64(rec.Count)
	sl.lastNano = rec.TimeUnixNano
	a.replaceHistogram(sl, rec.Extra)
}

// temporalityDelta is the stored name for delta aggregation temporality.
const temporalityDelta = "delta"

// replaceHistogram stores a cumulative snapshot as the slot's distribution.
func (a *MetricAccumulator) replaceHistogram(sl *metricSlot, extra json.RawMessage) {
	var h struct {
		Bounds []float64 `json:"bounds"`
		Counts []float64 `json:"counts"`
	}
	if len(extra) == 0 || json.Unmarshal(extra, &h) != nil {
		return
	}
	sl.histBounds = append([]float64(nil), h.Bounds...)
	sl.histCounts = append([]float64(nil), h.Counts...)
	sl.haveHist = true
}

// foldHistogram merges an explicit-bucket histogram's counts into the slot.
// Buckets with matching bounds add elementwise; a differing shape replaces
// (best effort — bounds are stable per series in practice).
func (a *MetricAccumulator) foldHistogram(sl *metricSlot, extra json.RawMessage) {
	var h struct {
		Bounds []float64 `json:"bounds"`
		Counts []float64 `json:"counts"`
	}
	if len(extra) == 0 || json.Unmarshal(extra, &h) != nil {
		return
	}
	if !sl.haveHist || len(sl.histCounts) != len(h.Counts) {
		sl.histBounds = append([]float64(nil), h.Bounds...)
		sl.histCounts = append([]float64(nil), h.Counts...)
		sl.haveHist = true
		return
	}
	for i := range h.Counts {
		sl.histCounts[i] += h.Counts[i]
	}
}

// Flush writes all closed buckets to SQLite and removes them from memory. Open
// buckets (those whose window has not yet elapsed) are kept so later points fold
// into the same row.
func (a *MetricAccumulator) Flush(ctx context.Context) error {
	cutoff := a.now().Add(-a.interval)

	a.mu.Lock()
	var rollups []repository.MetricRollup
	for k, sl := range a.slots {
		if k.bucket.After(cutoff) {
			continue // bucket still open
		}
		rollups = append(rollups, a.toRollup(k, sl))
		delete(a.slots, k)
	}
	a.mu.Unlock()

	if len(rollups) == 0 {
		return nil
	}
	if err := a.repo.UpsertMetricRollups(rollups); err != nil {
		return fmt.Errorf("metric accumulator flush: %w", err)
	}
	a.logger.Info("persisted metric rollups", "count", len(rollups))
	if a.onPersist != nil {
		a.onPersist(int64(len(rollups)))
	}
	return nil
}

func (a *MetricAccumulator) toRollup(k metricKey, sl *metricSlot) repository.MetricRollup {
	var extra string
	switch model.MetricType(sl.metricType) {
	case model.MetricTypeHistogram:
		if sl.haveHist {
			if b, err := json.Marshal(map[string]any{"bounds": sl.histBounds, "counts": sl.histCounts}); err == nil {
				extra = string(b)
			}
		}
	case model.MetricTypeExponentialHistogram, model.MetricTypeSummary:
		extra = string(sl.lastExtra)
	}
	return repository.MetricRollup{
		ProjectID:       k.projectID,
		Name:            k.name,
		Type:            sl.metricType,
		Unit:            sl.unit,
		Temporality:     sl.temporality,
		AttrFingerprint: k.fingerprint,
		Attributes:      sl.attributes,
		Bucket:          k.bucket,
		Count:           sl.count,
		Sum:             sl.sum,
		Min:             sl.min,
		Max:             sl.max,
		Last:            sl.last,
		ObsCount:        sl.obsCount,
		Extra:           extra,
	}
}

// Run flushes closed buckets on a ticker until ctx is cancelled, then performs a
// final flush of everything still buffered.
func (a *MetricAccumulator) Run(ctx context.Context) {
	ticker := time.NewTicker(a.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.flushAll(context.Background())
			return
		case <-ticker.C:
			if err := a.Flush(ctx); err != nil {
				a.logger.Warn("metric accumulator flush failed", "error", err)
			}
		}
	}
}

// flushAll drains every slot regardless of whether its bucket is closed. Used on
// shutdown so buffered data is not lost.
func (a *MetricAccumulator) flushAll(ctx context.Context) {
	a.mu.Lock()
	var rollups []repository.MetricRollup
	for k, sl := range a.slots {
		rollups = append(rollups, a.toRollup(k, sl))
	}
	a.slots = make(map[metricKey]*metricSlot)
	a.mu.Unlock()

	if len(rollups) == 0 {
		return
	}
	if err := a.repo.UpsertMetricRollups(rollups); err != nil {
		a.logger.Warn("metric accumulator final flush failed", "error", err)
		return
	}
	if a.onPersist != nil {
		a.onPersist(int64(len(rollups)))
	}
}
