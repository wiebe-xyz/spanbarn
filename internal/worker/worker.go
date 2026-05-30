package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

var tracer = otel.Tracer("spanbarn/worker")

const (
	DefaultBatchSize    = 1000
	DefaultTickInterval = 1 * time.Second
	maxRetries          = 5
)

// Repository is the interface the worker needs to persist spans.
type Repository interface {
	InsertSpans(ctx context.Context, spans []repository.Span) error
	InsertPromptRecords(ctx context.Context, records []repository.PromptRecord) error
}

// Aggregator is the interface the worker uses for inline aggregation.
// After each batch of spans is written, the worker aggregates them so
// the aggregates table stays current without waiting for retention.
type Aggregator interface {
	AggregateSpans(ctx context.Context, spans []repository.Span) ([]repository.Aggregate, error)
	Persist(ctx context.Context, aggregates []repository.Aggregate) error
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
	spool      *spool.Spool
	repo       Repository
	aggregator Aggregator
	logger     *slog.Logger
	metrics    Metrics
	retryBaseDelay time.Duration
}

// NewWorker creates a new background worker.
func NewWorker(sp *spool.Spool, repo Repository, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		spool:          sp,
		repo:           repo,
		logger:         logger,
		retryBaseDelay: 500 * time.Millisecond,
	}
}

// SetAggregator wires in an aggregator for inline aggregation after each batch.
func (w *Worker) SetAggregator(a Aggregator) {
	w.aggregator = a
}

// Run loops on a 1-second ticker, processing spool batches until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(DefaultTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
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

	_, insertSpan := tracer.Start(ctx, "worker.insert_spans")
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := w.repo.InsertSpans(ctx, spans); err != nil {
			lastErr = err
			w.logger.Info("worker: insert attempt failed",
				"attempt", attempt,
				"count", len(spans),
				"error", err,
			)
			backoff := time.Duration(attempt*attempt) * w.retryBaseDelay
			time.Sleep(backoff)
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

		// Inline aggregation: keep aggregates table current after every batch.
		if w.aggregator != nil {
			w.aggregateInline(ctx, spans)
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

func (w *Worker) aggregateInline(ctx context.Context, spans []repository.Span) {
	aggs, err := w.aggregator.AggregateSpans(ctx, spans)
	if err != nil {
		w.logger.Warn("worker: inline aggregate compute failed, retention will catch up", "error", err)
		return
	}
	if len(aggs) == 0 {
		return
	}
	if err := w.aggregator.Persist(ctx, aggs); err != nil {
		w.logger.Warn("worker: inline aggregate persist failed, retention will catch up", "error", err)
	}
}

// inlineAggInterval is how often the RedisWorker runs inline aggregation.
// Running after every batch causes too many write transactions under high ingest
// rates, which starves span inserts and backs up the Redis queue. Retention
// catches up on any spans skipped here within its own cycle.
const inlineAggInterval = 30 * time.Second

// RedisWorker consumes span batches from a Redis write queue and persists them
// to the repository. It reuses the same retry and inline-aggregation logic as
// the file-spool Worker.
type RedisWorker struct {
	queue         *queue.RedisQueue
	repo          Repository
	aggregator    Aggregator
	logger        *slog.Logger
	metrics       Metrics
	lastInlineAgg time.Time
	writeMu       *sync.Mutex
}

// NewRedisWorker creates a worker that drains the Redis write queue.
func NewRedisWorker(q *queue.RedisQueue, repo Repository, logger *slog.Logger) *RedisWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &RedisWorker{queue: q, repo: repo, logger: logger}
}

// SetAggregator wires in an aggregator for inline aggregation after each batch.
func (w *RedisWorker) SetAggregator(a Aggregator) {
	w.aggregator = a
}

// SetWriteMutex wires in the shared write serializer. When set, the worker
// holds the mutex for the entire write phase of each batch so that retention
// and span inserts never compete for the SQLite write lock.
func (w *RedisWorker) SetWriteMutex(mu *sync.Mutex) {
	w.writeMu = mu
}

// Run loops on BRPOP until ctx is cancelled.
func (w *RedisWorker) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		records, err := w.queue.Consume(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.logger.Error("redis worker: consume failed", "error", err)
			// Brief backoff to avoid spinning on a broken Redis connection.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if len(records) == 0 {
			// BRPOP timed out — no items, loop.
			continue
		}

		w.processBatch(ctx, records)
	}
}

func (w *RedisWorker) processBatch(ctx context.Context, records []model.SpanRecord) {
	ctx, span := tracer.Start(ctx, "redis_worker.process_batch")
	defer span.End()
	span.SetAttributes(attribute.Int("batch.size", len(records)))

	spans := convertRecords(records)

	// Acquire the shared write lock before touching the DB. This serialises
	// all writes with the retention worker so neither starves the other out
	// competing for the SQLite write connection.
	if w.writeMu != nil {
		w.writeMu.Lock()
		defer w.writeMu.Unlock()
	}

	_, insertSpan := tracer.Start(ctx, "redis_worker.insert_spans")
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := w.repo.InsertSpans(ctx, spans); err != nil {
			lastErr = err
			w.logger.Info("redis worker: insert attempt failed",
				"attempt", attempt,
				"count", len(spans),
				"error", err,
			)
			backoff := time.Duration(attempt*attempt) * 500 * time.Millisecond
			select {
			case <-ctx.Done():
				insertSpan.End()
				return
			case <-time.After(backoff):
			}
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		insertSpan.RecordError(lastErr)
		insertSpan.SetStatus(codes.Error, lastErr.Error())
	}
	insertSpan.End()

	if lastErr != nil {
		span.SetAttributes(attribute.Int("dead_lettered", len(spans)))
		w.logger.Error("redis worker: dead-lettering batch after retries",
			"count", len(spans),
			"error", lastErr,
		)
		w.metrics.mu.Lock()
		w.metrics.ErrorCount += int64(len(spans))
		w.metrics.mu.Unlock()
		return
	}

	w.metrics.mu.Lock()
	w.metrics.ProcessedCount += int64(len(spans))
	w.metrics.mu.Unlock()

	if promptRecs := extractPromptRecords(spans); len(promptRecs) > 0 {
		if err := w.repo.InsertPromptRecords(ctx, promptRecs); err != nil {
			w.logger.Warn("redis worker: insert prompt records", "count", len(promptRecs), "error", err)
		}
	}

	if w.aggregator != nil && time.Since(w.lastInlineAgg) >= inlineAggInterval {
		aggs, err := w.aggregator.AggregateSpans(ctx, spans)
		if err != nil {
			w.logger.Warn("redis worker: inline aggregate failed", "error", err)
			return
		}
		if len(aggs) > 0 {
			if err := w.aggregator.Persist(ctx, aggs); err != nil {
				w.logger.Warn("redis worker: inline aggregate persist failed", "error", err)
				return
			}
		}
		w.lastInlineAgg = time.Now()
	}
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
