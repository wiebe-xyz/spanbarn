package aggregation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Accumulator is an in-memory aggregate store fed span-by-span from the worker
// hot path. Every Add call is O(1) amortised. A background flush loop (Run)
// periodically writes accumulated slots to SQLite and resets them.
// QueryRecent lets the query service read current slots as synthetic aggregate
// rows, replacing the raw-span fallback in query_services.go.
//
// Accumulator also implements the AggregateSpans + Persist contract expected by
// the retention worker, so it can replace *Aggregator at the call site in
// main.go without changing retention internals.
type Accumulator struct {
	mu            sync.Mutex
	slots         map[accKey]*accSlot
	interval      time.Duration
	flushInterval time.Duration
	repo          AggregateWriter
	logger        *slog.Logger
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

// Add records a single span into its in-memory slot. Thread-safe; O(1) amortised.
func (a *Accumulator) Add(s repository.Span) {
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
	if s.Status == "error" {
		sl.errorCount++
	}
	sl.durations = append(sl.durations, s.DurationUs)
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
		durations := make([]int64, len(sl.durations))
		copy(durations, sl.durations)
		result = append(result, repository.Aggregate{
			ProjectID:  k.projectID,
			Service:    k.service,
			Operation:  k.operation,
			Resource:   k.resource,
			Kind:       k.kind,
			Bucket:     k.bucket,
			Count:      sl.count,
			ErrorCount: sl.errorCount,
			P50Us:      P50(durations),
			P95Us:      P95(durations),
			P99Us:      P99(durations),
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
		aggs = append(aggs, repository.Aggregate{
			ProjectID:     k.projectID,
			Service:       k.service,
			Operation:     k.operation,
			Resource:      k.resource,
			Kind:          k.kind,
			Bucket:        k.bucket,
			Count:         sl.count,
			ErrorCount:    sl.errorCount,
			P50Us:         P50(sl.durations),
			P95Us:         P95(sl.durations),
			P99Us:         P99(sl.durations),
			MaxUs:         maxUs,
			SumDurationUs: sumUs,
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

// AggregateSpans satisfies the retention.Aggregator interface. It computes
// aggregates from an arbitrary span batch without touching the in-memory slots —
// the retention worker uses this when aggregating spans it is about to delete.
func (a *Accumulator) AggregateSpans(ctx context.Context, spans []repository.Span) ([]repository.Aggregate, error) {
	if len(spans) == 0 {
		return nil, nil
	}

	_, otelSpan := tracer.Start(ctx, "accumulator.aggregate_spans")
	otelSpan.SetAttributes(attribute.Int("input_span_count", len(spans)))
	defer otelSpan.End()

	type batchKey struct {
		projectID int64
		service   string
		operation string
		resource  string
		kind      string
		bucket    time.Time
	}
	groups := make(map[batchKey][]repository.Span)
	for _, s := range spans {
		bucket := TruncateToBucket(time.UnixMicro(s.StartTimeUs), a.interval)
		k := batchKey{s.ProjectID, s.Service, s.Name, s.Resource, s.Kind, bucket}
		groups[k] = append(groups[k], s)
	}

	otelSpan.SetAttributes(attribute.Int("bucket_count", len(groups)))

	out := make([]repository.Aggregate, 0, len(groups))
	for k, batch := range groups {
		durations := make([]int64, len(batch))
		var errorCount, sumDuration, maxDuration int64
		for i, s := range batch {
			durations[i] = s.DurationUs
			sumDuration += s.DurationUs
			if s.DurationUs > maxDuration {
				maxDuration = s.DurationUs
			}
			if s.Status == "error" {
				errorCount++
			}
		}
		out = append(out, repository.Aggregate{
			ProjectID:     k.projectID,
			Service:       k.service,
			Operation:     k.operation,
			Resource:      k.resource,
			Kind:          k.kind,
			Bucket:        k.bucket,
			Count:         int64(len(batch)),
			ErrorCount:    errorCount,
			P50Us:         P50(durations),
			P95Us:         P95(durations),
			P99Us:         P99(durations),
			MaxUs:         maxDuration,
			SumDurationUs: sumDuration,
		})
	}
	return out, nil
}

// Persist satisfies the retention.Aggregator interface.
func (a *Accumulator) Persist(ctx context.Context, aggregates []repository.Aggregate) error {
	_, otelSpan := tracer.Start(ctx, "accumulator.persist")
	otelSpan.SetAttributes(attribute.Int("aggregate_count", len(aggregates)))
	defer otelSpan.End()

	if err := a.repo.UpsertAggregates(aggregates); err != nil {
		otelSpan.RecordError(err)
		otelSpan.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("persist %d aggregates: %w", len(aggregates), err)
	}
	a.logger.Info("persisted aggregates", "count", len(aggregates))
	return nil
}
