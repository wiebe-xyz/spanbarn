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
	spool          *spool.Spool
	repo           Repository
	aggregator     Aggregator
	logger         *slog.Logger
	metrics        Metrics
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

// SpanAccumulator receives every span for in-memory aggregation.
// Satisfied by *aggregation.Accumulator.
type SpanAccumulator interface {
	Add(s repository.Span)
}

// WorkerConfig holds tunable parameters for RedisWorker.
type WorkerConfig struct {
	// SlowThresholdUs is the duration above which a span is considered "slow"
	// and therefore interesting (must be stored in SQLite). Spans below this
	// threshold with no errors are boring and skipped. 0 disables the filter
	// (all spans are written).
	SlowThresholdUs int64
}

// RedisWorker consumes span batches from a Redis write queue and persists
// interesting spans to the repository. Boring spans (no error, below the slow
// threshold) are fed only to the in-memory accumulator and never written to
// SQLite, keeping the spans table small and fast.
type RedisWorker struct {
	queue        *queue.RedisQueue
	repo         Repository
	accumulator  SpanAccumulator
	boringPolicy BoringPolicyReader
	logger       *slog.Logger
	metrics      Metrics
	writeMu      *sync.Mutex
	cfg          WorkerConfig
}

// NewRedisWorker creates a worker that drains the Redis write queue.
func NewRedisWorker(q *queue.RedisQueue, repo Repository, logger *slog.Logger) *RedisWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &RedisWorker{queue: q, repo: repo, logger: logger}
}

// SetAccumulator wires in the in-memory accumulator. Every span in each batch
// (boring and interesting alike) is fed to Add before SQLite classification.
func (w *RedisWorker) SetAccumulator(a SpanAccumulator) {
	w.accumulator = a
}

// SetConfig applies worker tuning parameters.
func (w *RedisWorker) SetConfig(cfg WorkerConfig) {
	w.cfg = cfg
}

// SetBoringPolicy wires in the per-project boring span policy (sampling + verbose mode).
func (w *RedisWorker) SetBoringPolicy(p BoringPolicyReader) {
	w.boringPolicy = p
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

	// Feed every span to the accumulator for in-memory aggregation, including
	// boring spans that will not reach SQLite.
	if w.accumulator != nil {
		for i := range spans {
			w.accumulator.Add(spans[i])
		}
	}

	// Classify: interesting spans (error, slow, or verbose-project) are written to
	// SQLite unconditionally; boring spans may be sampled based on per-project policy.
	interesting := w.classifyForStorage(spans)

	boringCount := len(spans) - len(interesting)
	if boringCount > 0 {
		span.SetAttributes(attribute.Int("boring_skipped", boringCount))
	}

	if len(interesting) == 0 {
		return
	}

	// Acquire the shared write lock before touching the DB.
	if w.writeMu != nil {
		w.writeMu.Lock()
		defer w.writeMu.Unlock()
	}

	_, insertSpan := tracer.Start(ctx, "redis_worker.insert_spans")
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := w.repo.InsertSpans(ctx, interesting); err != nil {
			lastErr = err
			w.logger.Info("redis worker: insert attempt failed",
				"attempt", attempt,
				"count", len(interesting),
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
		span.SetAttributes(attribute.Int("dead_lettered", len(interesting)))
		w.logger.Error("redis worker: dead-lettering batch after retries",
			"count", len(interesting),
			"error", lastErr,
		)
		w.metrics.mu.Lock()
		w.metrics.ErrorCount += int64(len(interesting))
		w.metrics.mu.Unlock()
		return
	}

	w.metrics.mu.Lock()
	w.metrics.ProcessedCount += int64(len(interesting))
	w.metrics.mu.Unlock()

	if promptRecs := extractPromptRecords(interesting); len(promptRecs) > 0 {
		if err := w.repo.InsertPromptRecords(ctx, promptRecs); err != nil {
			w.logger.Warn("redis worker: insert prompt records", "count", len(promptRecs), "error", err)
		}
	}
}

// classifyForStorage builds the set of spans to write to SQLite.
// All spans in error/slow traces are included unconditionally. Spans in
// verbose-mode projects bypass boring classification. Remaining boring spans
// are sampled at the per-project ratio from boringPolicy (if wired).
func (w *RedisWorker) classifyForStorage(spans []repository.Span) []repository.Span {
	if w.cfg.SlowThresholdUs <= 0 {
		return spans
	}

	now := time.Now()

	// Determine which projects are in verbose mode (record all spans).
	verboseProject := make(map[int64]bool, 2)
	if w.boringPolicy != nil {
		for _, s := range spans {
			if _, seen := verboseProject[s.ProjectID]; !seen {
				until := w.boringPolicy.VerboseUntil(s.ProjectID)
				verboseProject[s.ProjectID] = !until.IsZero() && now.Before(until)
			}
		}
	}

	// First pass: find trace IDs that are definitely interesting.
	interestingTraces := make(map[string]struct{}, len(spans))
	for _, s := range spans {
		if verboseProject[s.ProjectID] || s.Status == "error" || s.DurationUs > w.cfg.SlowThresholdUs {
			interestingTraces[s.TraceID] = struct{}{}
		}
	}

	// Second pass: separate interesting from boring.
	result := make([]repository.Span, 0, len(spans))
	boring := make([]repository.Span, 0, 8)
	for _, s := range spans {
		if _, ok := interestingTraces[s.TraceID]; ok {
			result = append(result, s)
		} else {
			boring = append(boring, s)
		}
	}

	// Sample boring spans per project policy.
	if w.boringPolicy == nil || len(boring) == 0 {
		return result
	}
	byProject := make(map[int64][]repository.Span, 2)
	for _, s := range boring {
		byProject[s.ProjectID] = append(byProject[s.ProjectID], s)
	}
	for projID, projectBoring := range byProject {
		ratio := w.boringPolicy.SampleRatio(projID)
		switch {
		case ratio == 1:
			result = append(result, projectBoring...)
		case ratio > 1:
			for _, s := range projectBoring {
				if rand.IntN(ratio) == 0 {
					result = append(result, s)
				}
			}
			// ratio == 0: skip all boring spans for this project
		}
	}
	return result
}

// classifyInteresting returns only the spans that should be written to SQLite:
// those that are errors or slow, plus any boring span that shares a trace_id
// with an interesting span (preserves waterfall completeness within a batch).
// Used by tests directly; production code uses classifyForStorage.
func classifyInteresting(spans []repository.Span, slowThresholdUs int64) []repository.Span {
	interestingTraces := make(map[string]struct{}, len(spans))
	for _, s := range spans {
		if s.Status == "error" || s.DurationUs > slowThresholdUs {
			interestingTraces[s.TraceID] = struct{}{}
		}
	}
	if len(interestingTraces) == 0 {
		return nil
	}
	result := make([]repository.Span, 0, len(spans))
	for _, s := range spans {
		if _, ok := interestingTraces[s.TraceID]; ok {
			result = append(result, s)
		}
	}
	return result
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
