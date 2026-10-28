package aggregation

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// SpanRollupStep is the width of the tier the accumulator writes, in seconds.
const SpanRollupStep int64 = 300

// OtherName replaces a service or span name past its cardinality cap.
const OtherName = "~other"

// spanRollupRequeueAge bounds how old a bucket a failed flush puts back. A
// write path that stays broken would otherwise grow the slots without end.
const spanRollupRequeueAge = time.Hour

// SpanRollupWriter persists span rollup and span edge rows. Satisfied by
// *repository.Repository.
type SpanRollupWriter interface {
	AddSpanRollups(rows []repository.SpanRollup) error
	AddSpanEdges(rows []repository.SpanEdge) error
}

// SpanRollupConfig caps the rows one hour of one project can create.
type SpanRollupConfig struct {
	// MaxNames is the number of distinct span names per (project, service)
	// per hour. Later names fold into OtherName. Default 300.
	MaxNames int
	// MaxServices is the number of distinct services per project per hour.
	// Later services, and their names, fold into OtherName. Default 200.
	MaxServices int
	// MaxEdgeTargets is the number of distinct call targets per (project,
	// source service) per hour in span_edges. Later targets fold into
	// OtherName. Default 200.
	MaxEdgeTargets int
	// FlushInterval is how often Run writes the open buckets. Default 30s.
	FlushInterval time.Duration
	// HTTPStatus splits root rows by the span's response code. Off writes 0
	// for every span, which keeps the key and turns the split off.
	HTTPStatus bool
}

func (c SpanRollupConfig) withDefaults() SpanRollupConfig {
	if c.MaxNames <= 0 {
		c.MaxNames = 300
	}
	if c.MaxServices <= 0 {
		c.MaxServices = 200
	}
	if c.MaxEdgeTargets <= 0 {
		c.MaxEdgeTargets = 200
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = 30 * time.Second
	}
	return c
}

// SpanRollups folds every span it is given into 5-minute buckets of
// span_rollups: counts, sample weights, duration sum, min, max and a
// quarter-octave histogram per (project, service, name, kind, root, status,
// HTTP status of a root span).
// A bucket is keyed by arrival time. Each flush adds what arrived since the
// last one into the stored rows, so the open bucket is written on every flush
// and a restart loses one flush interval at most. AddBatch also folds the
// batch's calls into span_edges, flushed with the rollups.
type SpanRollups struct {
	mu     sync.Mutex
	slots  map[spanRollupKey]*spanRollupSlot
	caps   rollupCaps
	writer SpanRollupWriter
	cfg    SpanRollupConfig
	logger *slog.Logger
	now    func() time.Time
	edges  *spanEdges
}

type spanRollupKey struct {
	bucket    int64
	projectID int64
	service   string
	name      string
	kind      string
	root      bool
	status    string
	http      int64
}

// durationStats is what a span rollup or span edge slot holds: the spans,
// their sample weights and their durations.
type durationStats struct {
	count         int64
	weights       spanWeights
	sum, min, max int64
	hist          spanhist.Hist
}

func (d *durationStats) observe(s repository.Span) {
	if d.count == 0 {
		d.min, d.max = s.DurationUs, s.DurationUs
	}
	d.count++
	d.weights.add(s)
	d.sum += s.DurationUs
	d.min = min(d.min, s.DurationUs)
	d.max = max(d.max, s.DurationUs)
	d.hist.Observe(s.DurationUs)
}

func (d *durationStats) merge(o durationStats) {
	d.count += o.count
	d.weights.count += o.weights.count
	d.weights.spans += o.weights.spans
	d.sum += o.sum
	d.min = min(d.min, o.min)
	d.max = max(d.max, o.max)
	d.hist.Merge(o.hist)
}

type spanRollupSlot struct{ durationStats }

// NewSpanRollups creates a SpanRollups that writes through w.
func NewSpanRollups(w SpanRollupWriter, cfg SpanRollupConfig, logger *slog.Logger) *SpanRollups {
	if logger == nil {
		logger = slog.Default()
	}
	r := &SpanRollups{
		slots:  map[spanRollupKey]*spanRollupSlot{},
		caps:   newRollupCaps(),
		writer: w,
		cfg:    cfg.withDefaults(),
		logger: logger,
		now:    time.Now,
	}
	r.edges = newSpanEdges(w, r.cfg, logger, func() time.Time { return r.now() })
	return r
}

// AddBatch folds every span of a batch into its slot, and the calls the batch
// shows into span_edges. Safe for concurrent use.
func (r *SpanRollups) AddBatch(spans []repository.Span) {
	for i := range spans {
		r.Add(spans[i])
	}
	r.edges.add(spans)
}

// Add folds one span into its slot. It records no calls: those need the
// span's batch (AddBatch). Safe for concurrent use.
func (r *SpanRollups) Add(s repository.Span) {
	bucket := arrivalBucket(s, r.now)
	var code int64
	if s.ParentSpanID == "" && r.cfg.HTTPStatus {
		code = httpStatusOf(s.Attributes) // outside the lock: it scans the attributes
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	service, name, first := r.caps.admit(s.ProjectID, s.Service, s.Name, bucket, r.cfg)
	if first {
		r.logger.Info("span rollups: cardinality cap reached, folding the rest of the hour into "+OtherName,
			"project_id", s.ProjectID, "service", s.Service, "max_names", r.cfg.MaxNames, "max_services", r.cfg.MaxServices)
	}
	k := spanRollupKey{
		bucket:    bucket,
		projectID: s.ProjectID,
		service:   service,
		name:      name,
		kind:      strings.ToLower(s.Kind),
		root:      s.ParentSpanID == "",
		status:    rollupStatus(s.Status),
		http:      code,
	}
	sl := r.slots[k]
	if sl == nil {
		sl = &spanRollupSlot{}
		r.slots[k] = sl
	}
	sl.observe(s)
}

// rollupStatus maps a span status onto ok, error or unset.
func rollupStatus(status string) string {
	switch strings.ToLower(status) {
	case "error":
		return "error"
	case "ok":
		return "ok"
	default:
		return "unset"
	}
}

// Flush writes the span rollups and then the span edges. Each that fails
// keeps its slots for the next flush.
func (r *SpanRollups) Flush() error {
	return errors.Join(r.flushRollups(), r.edges.flush())
}

// flushRollups writes every slot and starts new ones. When the write fails
// the slots go back, merged with whatever arrived meanwhile, unless their
// bucket is older than spanRollupRequeueAge.
func (r *SpanRollups) flushRollups() error {
	r.mu.Lock()
	slots := r.slots
	r.slots = map[spanRollupKey]*spanRollupSlot{}
	r.caps.prune(r.now().Unix())
	r.mu.Unlock()
	if len(slots) == 0 {
		return nil
	}

	rows := make([]repository.SpanRollup, 0, len(slots))
	for k, sl := range slots {
		rows = append(rows, sl.row(k))
	}
	start := r.now()
	if err := r.writer.AddSpanRollups(rows); err != nil {
		r.requeue(slots)
		return err
	}
	r.logger.Info("persisted span rollups", "rows", len(rows), "duration_ms", r.now().Sub(start).Milliseconds())
	return nil
}

func (sl *spanRollupSlot) row(k spanRollupKey) repository.SpanRollup {
	return repository.SpanRollup{
		StepSeconds:   SpanRollupStep,
		Bucket:        time.Unix(k.bucket, 0).UTC(),
		ProjectID:     k.projectID,
		Service:       k.service,
		Name:          k.name,
		Kind:          k.kind,
		IsRoot:        k.root,
		Status:        k.status,
		HTTPStatus:    k.http,
		Count:         sl.count,
		WeightedCount: sl.weights.count,
		WeightedSpans: sl.weights.spans,
		SumDurationUs: sl.sum,
		MinUs:         sl.min,
		MaxUs:         sl.max,
		Hist:          sl.hist.Encode(),
	}
}

func (r *SpanRollups) requeue(slots map[spanRollupKey]*spanRollupSlot) {
	oldest := r.now().Add(-spanRollupRequeueAge).Unix()
	r.mu.Lock()
	defer r.mu.Unlock()
	requeueSlots(r.slots, slots, func(k spanRollupKey) bool { return k.bucket >= oldest },
		func(cur, sl *spanRollupSlot) { cur.merge(sl.durationStats) })
}

// requeueSlots puts the slots of a failed flush back into dst, merged with
// any that arrived meanwhile, except those keep refuses.
func requeueSlots[K comparable, V any](dst, src map[K]*V, keep func(K) bool, merge func(cur, sl *V)) {
	for k, sl := range src {
		if !keep(k) {
			continue
		}
		if cur := dst[k]; cur != nil {
			merge(cur, sl)
		} else {
			dst[k] = sl
		}
	}
}

// Run flushes on a ticker until ctx is cancelled.
func (r *SpanRollups) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Flush(); err != nil {
				r.logger.Warn("span rollup flush failed, slots kept for the next flush", "error", err)
			}
		}
	}
}
