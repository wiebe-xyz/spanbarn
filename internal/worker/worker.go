package worker

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

var tracer = otel.Tracer("spanbarn/worker")

const (
	// DefaultBatchSize is the maximum number of records to process per tick.
	DefaultBatchSize = 1000
	// DefaultTickInterval is the interval between worker ticks.
	DefaultTickInterval = 1 * time.Second
	// maxRetries is the number of times a failing record batch is retried before being skipped.
	maxRetries = 3
)

// Repository is the interface the worker needs to persist spans.
type Repository interface {
	InsertSpans(ctx context.Context, spans []repository.Span) error
	InsertPromptRecords(ctx context.Context, records []repository.PromptRecord) error
}

// Metrics tracks worker processing stats.
type Metrics struct {
	mu                 sync.Mutex
	ProcessedCount     int64
	ErrorCount         int64
	ProcessingDuration time.Duration
}

// Snapshot returns a copy of the current metrics.
func (m *Metrics) Snapshot() (processed, errors int64, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ProcessedCount, m.ErrorCount, m.ProcessingDuration
}

// Worker reads from the spool and persists records to the repository.
type Worker struct {
	spool            *spool.Spool
	repo             Repository
	logger           *slog.Logger
	metrics          Metrics
	ingestSampleRate float64
	slowThresholdUS  int64
}

// NewWorker creates a new background worker.
func NewWorker(sp *spool.Spool, repo Repository, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		spool:            sp,
		repo:             repo,
		logger:           logger,
		ingestSampleRate: 1.0,
	}
}

// SetIngestSampling configures probabilistic dropping of uneventful spans.
// rate=1.0 keeps all spans (default). rate=0.5 drops ~50% of non-error, non-slow spans.
func (w *Worker) SetIngestSampling(rate float64, slowThresholdUS int64) {
	w.ingestSampleRate = rate
	w.slowThresholdUS = slowThresholdUS
}

// Run loops on a 1-second ticker, processing spool batches until ctx is cancelled.
// It finishes the current batch before returning.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(DefaultTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Finish one last batch before exiting.
			w.processBatch(ctx)
			return
		case <-ticker.C:
			w.processBatch(ctx)
		}
	}
}

// ProcessOnce runs a single processing cycle. Useful for testing.
func (w *Worker) ProcessOnce(ctx context.Context) {
	w.processBatch(ctx)
}

// GetMetrics returns a snapshot of the worker's metrics.
func (w *Worker) GetMetrics() (processed, errors int64, duration time.Duration) {
	return w.metrics.Snapshot()
}

func (w *Worker) processBatch(ctx context.Context) {
	start := time.Now()

	cursor, err := w.spool.LoadCursor()
	if err != nil {
		w.logger.Error("worker: load cursor", "error", err)
		w.metrics.mu.Lock()
		w.metrics.ErrorCount++
		w.metrics.mu.Unlock()
		return
	}

	records, nextCursor, err := w.spool.Read(cursor, DefaultBatchSize)
	if err != nil {
		w.logger.Error("worker: read spool", "error", err)
		w.metrics.mu.Lock()
		w.metrics.ErrorCount++
		w.metrics.mu.Unlock()
		return
	}

	if len(records) == 0 {
		return
	}

	ctx, span := tracer.Start(ctx, "worker.process_batch")
	defer span.End()
	span.SetAttributes(attribute.Int("batch.size", len(records)))

	spans := convertRecords(records)

	if w.ingestSampleRate < 1.0 {
		spans = w.sampleSpans(spans)
		span.SetAttributes(attribute.Int("batch.size_after_sampling", len(spans)))
	}

	_, insertSpan := tracer.Start(ctx, "worker.insert_spans")
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := w.repo.InsertSpans(ctx, spans); err != nil {
			lastErr = err
			w.logger.Warn("worker: insert attempt failed",
				"attempt", attempt,
				"count", len(spans),
				"error", err,
			)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		insertSpan.RecordError(lastErr)
		insertSpan.SetStatus(codes.Error, lastErr.Error())
		insertSpan.SetAttributes(attribute.Bool("retries_exhausted", true))
	}
	insertSpan.End()

	if lastErr != nil {
		span.SetAttributes(attribute.Int("dead_lettered", len(spans)))
		w.logger.Error("worker: dead-lettering batch after retries",
			"count", len(spans),
			"error", lastErr,
		)
		w.metrics.mu.Lock()
		w.metrics.ErrorCount += int64(len(spans))
		w.metrics.mu.Unlock()
	} else {
		w.metrics.mu.Lock()
		w.metrics.ProcessedCount += int64(len(spans))
		w.metrics.mu.Unlock()

		if promptRecs := extractPromptRecords(spans); len(promptRecs) > 0 {
			if err := w.repo.InsertPromptRecords(ctx, promptRecs); err != nil {
				w.logger.Warn("worker: insert prompt records", "count", len(promptRecs), "error", err)
			}
		}
	}

	if err := w.spool.SaveCursor(nextCursor); err != nil {
		w.logger.Error("worker: save cursor", "error", err)
	}

	elapsed := time.Since(start)
	span.SetAttributes(attribute.Int64("duration_ms", elapsed.Milliseconds()))
	w.metrics.mu.Lock()
	w.metrics.ProcessingDuration += elapsed
	w.metrics.mu.Unlock()
}

// sampleSpans keeps all interesting spans (error/slow) and probabilistically
// drops normal spans based on the configured sample rate.
func (w *Worker) sampleSpans(spans []repository.Span) []repository.Span {
	kept := make([]repository.Span, 0, len(spans))
	for _, s := range spans {
		if s.Status == "error" || (w.slowThresholdUS > 0 && s.DurationUs > w.slowThresholdUS) {
			kept = append(kept, s)
			continue
		}
		if rand.Float64() < w.ingestSampleRate {
			kept = append(kept, s)
		}
	}
	return kept
}

func convertRecords(records []model.SpanRecord) []repository.Span {
	spans := make([]repository.Span, len(records))
	for i, r := range records {
		attrs := string(r.Attributes)
		if attrs == "" {
			attrs = "{}"
		}
		events := string(r.Events)
		if events == "" {
			events = "[]"
		}
		spans[i] = repository.Span{
			ProjectID:    r.ProjectID,
			TraceID:      r.TraceID,
			SpanID:       r.SpanID,
			ParentSpanID: r.ParentSpanID,
			Name:         r.Name,
			Service:      r.Service,
			Resource:     r.Resource,
			Kind:         r.Kind,
			Status:       r.Status,
			StartTimeUs:  r.StartTimeUs,
			DurationUs:   r.DurationUs,
			Attributes:   attrs,
			Events:       events,
		}
	}
	return spans
}
