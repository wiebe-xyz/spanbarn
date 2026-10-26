package aggregation

import (
	"context"
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

// SpanRollupWriter persists span rollup rows. Satisfied by
// *repository.Repository.
type SpanRollupWriter interface {
	AddSpanRollups(rows []repository.SpanRollup) error
}

// SpanRollupConfig caps the rows one hour of one project can create.
type SpanRollupConfig struct {
	// MaxNames is the number of distinct span names per (project, service)
	// per hour. Later names fold into OtherName. Default 300.
	MaxNames int
	// MaxServices is the number of distinct services per project per hour.
	// Later services, and their names, fold into OtherName. Default 200.
	MaxServices int
	// FlushInterval is how often Run writes the open buckets. Default 30s.
	FlushInterval time.Duration
}

func (c SpanRollupConfig) withDefaults() SpanRollupConfig {
	if c.MaxNames <= 0 {
		c.MaxNames = 300
	}
	if c.MaxServices <= 0 {
		c.MaxServices = 200
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = 30 * time.Second
	}
	return c
}

// SpanRollups folds every span it is given into 5-minute buckets of
// span_rollups: counts, sample weights, duration sum, min, max and a
// quarter-octave histogram per (project, service, name, kind, root, status).
// A bucket is keyed by arrival time. Each flush adds what arrived since the
// last one into the stored rows, so the open bucket is written on every flush
// and a restart loses one flush interval at most.
type SpanRollups struct {
	mu     sync.Mutex
	slots  map[spanRollupKey]*spanRollupSlot
	caps   rollupCaps
	writer SpanRollupWriter
	cfg    SpanRollupConfig
	logger *slog.Logger
	now    func() time.Time
}

type spanRollupKey struct {
	bucket    int64
	projectID int64
	service   string
	name      string
	kind      string
	root      bool
	status    string
}

type spanRollupSlot struct {
	count   int64
	weights spanWeights
	sum     int64
	min     int64
	max     int64
	hist    spanhist.Hist
}

// NewSpanRollups creates a SpanRollups that writes through w.
func NewSpanRollups(w SpanRollupWriter, cfg SpanRollupConfig, logger *slog.Logger) *SpanRollups {
	if logger == nil {
		logger = slog.Default()
	}
	return &SpanRollups{
		slots:  map[spanRollupKey]*spanRollupSlot{},
		caps:   newRollupCaps(),
		writer: w,
		cfg:    cfg.withDefaults(),
		logger: logger,
		now:    time.Now,
	}
}

// Add folds one span into its slot. Safe for concurrent use.
func (r *SpanRollups) Add(s repository.Span) {
	at := s.IngestedAt
	if at.IsZero() {
		at = r.now()
	}
	bucket := at.Unix() - at.Unix()%SpanRollupStep
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
	}
	sl := r.slots[k]
	if sl == nil {
		sl = &spanRollupSlot{min: s.DurationUs, max: s.DurationUs}
		r.slots[k] = sl
	}
	sl.count++
	sl.weights.add(s)
	sl.sum += s.DurationUs
	sl.min = min(sl.min, s.DurationUs)
	sl.max = max(sl.max, s.DurationUs)
	sl.hist.Observe(s.DurationUs)
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

// Flush writes every slot and starts new ones. When the write fails the
// slots go back, merged with whatever arrived meanwhile, unless their bucket
// is older than spanRollupRequeueAge.
func (r *SpanRollups) Flush() error {
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
	for k, sl := range slots {
		if k.bucket < oldest {
			continue
		}
		cur := r.slots[k]
		if cur == nil {
			r.slots[k] = sl
			continue
		}
		cur.count += sl.count
		cur.weights.count += sl.weights.count
		cur.weights.spans += sl.weights.spans
		cur.sum += sl.sum
		cur.min = min(cur.min, sl.min)
		cur.max = max(cur.max, sl.max)
		cur.hist.Merge(sl.hist)
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
