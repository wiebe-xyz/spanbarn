package aggregation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Accumulator is an in-memory aggregate store fed span-by-span from the worker
// hot path. Every Add call is O(1) amortised. A background flush loop (Run)
// periodically writes accumulated slots to SQLite and resets them.
// QueryRecent lets the query service read current slots as synthetic aggregate
// rows, replacing the raw-span fallback in query_services.go.
type Accumulator struct {
	mu            sync.Mutex
	slots         map[accKey]*accSlot
	interval      time.Duration
	flushInterval time.Duration
	repo          AggregateWriter
	logger        *slog.Logger
	// rollups, when set, receives every span Add and AddBatch see
	// (span_rollups, and span_edges for batches).
	rollups *SpanRollups
}

type accKey struct {
	projectID int64
	service   string
	operation string
	resource  string
	kind      string
	bucket    time.Time
}

type accSlot struct {
	count      int64
	errorCount int64
	durations  []int64
	// sampleWeights runs parallel to durations; 0 marks an unknown weight.
	sampleWeights []int64
	weights       spanWeights
}

// NewAccumulator creates an Accumulator.
// interval is the bucket granularity (e.g. time.Minute).
// flushInterval controls how often accumulated slots are written to SQLite (e.g. 30s).
func NewAccumulator(repo AggregateWriter, interval, flushInterval time.Duration, logger *slog.Logger) *Accumulator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Accumulator{
		slots:         make(map[accKey]*accSlot),
		interval:      interval,
		flushInterval: flushInterval,
		repo:          repo,
		logger:        logger,
	}
}

// SetSpanRollups makes Add pass every span on to r as well.
func (a *Accumulator) SetSpanRollups(r *SpanRollups) { a.rollups = r }

// AddBatch records every span of a batch, and passes the batch on to the
// span rollups, which also read the calls between its spans.
func (a *Accumulator) AddBatch(spans []repository.Span) {
	if a.rollups != nil {
		a.rollups.AddBatch(spans)
	}
	for i := range spans {
		a.addSlot(spans[i])
	}
}

// Add records a single span into its in-memory slot. Thread-safe; O(1) amortised.
func (a *Accumulator) Add(s repository.Span) {
	if a.rollups != nil {
		a.rollups.Add(s)
	}
	a.addSlot(s)
}

func (a *Accumulator) addSlot(s repository.Span) {
	bucket := TruncateToBucket(time.UnixMicro(s.StartTimeUs), a.interval)
	k := accKey{
		projectID: s.ProjectID,
		service:   s.Service,
		operation: s.Name,
		resource:  s.Resource,
		kind:      s.Kind,
		bucket:    bucket,
	}
	a.mu.Lock()
	sl := a.slots[k]
	if sl == nil {
		sl = &accSlot{}
		a.slots[k] = sl
	}
	sl.count++
	sl.weights.add(s)
	if s.Status == "error" {
		sl.errorCount++
	}
	sl.durations = append(sl.durations, s.DurationUs)
	sl.sampleWeights = append(sl.sampleWeights, int64(s.SampleWeight))
	a.mu.Unlock()
}

// QueryRecent returns synthetic aggregate rows from the current in-memory slots
// that match f. Used by the query service to cover the recent tail window
// without scanning the spans table.
func (a *Accumulator) QueryRecent(f repository.AggregateFilter) []repository.Aggregate {
	a.mu.Lock()
	defer a.mu.Unlock()

	var result []repository.Aggregate
	for k, sl := range a.slots {
		if f.ProjectID != 0 && k.projectID != f.ProjectID {
			continue
		}
		if f.Service != "" && k.service != f.Service {
			continue
		}
		if f.Operation != "" && k.operation != f.Operation {
			continue
		}
		if f.Kind != "" && k.kind != f.Kind {
			continue
		}
		if !f.From.IsZero() && k.bucket.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && k.bucket.After(f.To) {
			continue
		}
		p50, p95, p99 := percentilesOf(sl.durations, sl.sampleWeights)
		result = append(result, repository.Aggregate{
			ProjectID:     k.projectID,
			Service:       k.service,
			Operation:     k.operation,
			Resource:      k.resource,
			Kind:          k.kind,
			Bucket:        k.bucket,
			Count:         sl.count,
			ErrorCount:    sl.errorCount,
			P50Us:         p50,
			P95Us:         p95,
			P99Us:         p99,
			WeightedCount: sl.weights.count,
			WeightedSpans: sl.weights.spans,
		})
	}
	return result
}

// Flush drains all accumulated slots, computes final aggregates, and upserts
// them into SQLite. Slots are swapped out atomically so Add calls during Flush
// land in the next cycle without blocking.
func (a *Accumulator) Flush(ctx context.Context) error {
	a.mu.Lock()
	slots := a.slots
	a.slots = make(map[accKey]*accSlot)
	a.mu.Unlock()

	if len(slots) == 0 {
		return nil
	}

	aggs := make([]repository.Aggregate, 0, len(slots))
	for k, sl := range slots {
		var maxUs, sumUs int64
		for _, d := range sl.durations {
			sumUs += d
			if d > maxUs {
				maxUs = d
			}
		}
		p50, p95, p99 := percentilesOf(sl.durations, sl.sampleWeights)
		aggs = append(aggs, repository.Aggregate{
			ProjectID:     k.projectID,
			Service:       k.service,
			Operation:     k.operation,
			Resource:      k.resource,
			Kind:          k.kind,
			Bucket:        k.bucket,
			Count:         sl.count,
			ErrorCount:    sl.errorCount,
			P50Us:         p50,
			P95Us:         p95,
			P99Us:         p99,
			MaxUs:         maxUs,
			SumDurationUs: sumUs,
			WeightedCount: sl.weights.count,
			WeightedSpans: sl.weights.spans,
		})
	}

	if err := a.repo.UpsertAggregates(aggs); err != nil {
		return fmt.Errorf("accumulator flush: %w", err)
	}
	a.logger.Info("persisted aggregates", "count", len(aggs))
	return nil
}

// Run starts a periodic flush loop. Blocks until ctx is cancelled.
func (a *Accumulator) Run(ctx context.Context) {
	ticker := time.NewTicker(a.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.Flush(ctx); err != nil {
				a.logger.Warn("accumulator flush failed", "error", err)
			}
		}
	}
}
