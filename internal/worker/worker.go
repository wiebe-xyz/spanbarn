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
	"github.com/wiebe-xyz/spanbarn/internal/sampling"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

var tracer = otel.Tracer("spanbarn/worker")

const (
	DefaultBatchSize    = 1000
	DefaultTickInterval = 1 * time.Second
	maxRetries          = 5
	// diskFullBackoff is how long the worker pauses after requeueing a batch
	// the disk had no room for, so it does not spin against a full volume
	// while retention's emergency eviction reclaims space.
	diskFullBackoff = 5 * time.Second
)

// insertRetryBackoff is the base unit of the quadratic backoff between insert
// attempts. A variable rather than a constant so tests can exercise the full
// retry budget without spending ~27s of real sleeping.
var insertRetryBackoff = 500 * time.Millisecond

// Repository is the interface the worker needs to persist spans.
type Repository interface {
	InsertSpans(ctx context.Context, spans []repository.Span) error
	InsertSpansStaging(ctx context.Context, spans []repository.Span) error
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
	// BoringRetention is how long a sampled-boring span is kept before the boring
	// cleanup may delete it (stamped as expires_at at classification). 0 leaves
	// expires_at unset, so boring spans fall back to the aggregate-then-delete pass.
	BoringRetention time.Duration
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
	floor        *sampling.MinuteFloor
	logger       *slog.Logger
	metrics      Metrics
	cfg          WorkerConfig
	// stageOnly, when set, makes the worker append every consumed span to the
	// spans_staging table and return immediately, deferring accumulation,
	// classification and indexed storage to the StagingFlusher. This keeps the
	// Redis drain fast (cheap unindexed appends) so the queue never backs up.
	stageOnly bool
}

// SetStagingMode switches the worker to append consumed spans to spans_staging
// instead of accumulating/classifying/inserting inline. The StagingFlusher then
// does that work off the hot path.
func (w *RedisWorker) SetStagingMode(on bool) {
	w.stageOnly = on
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

// SetMinuteFloor wires in the per-(project, operation) survival floor that
// guarantees a minimum number of boring traces are stored each minute even when
// ratio sampling would otherwise drop them all. Must back a single writer to
// count accurately.
func (w *RedisWorker) SetMinuteFloor(f *sampling.MinuteFloor) {
	w.floor = f
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
			// Warn, not error: the connection drop is transient, the backoff
			// below handles it, and nothing has been lost. Logging it at error
			// filed a BugBarn issue for every Redis reconnect.
			w.logger.Warn("redis worker: consume failed, retrying", "error", err)
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

// insertWithRetry writes the batch, retrying transient failures with quadratic
// backoff. It returns the last error and whether that error was a full disk.
//
// A full disk short-circuits the loop: it is not transient, so retrying it
// maxRetries times with backoff spends seconds failing and then dead-letters
// the batch — discarding the very data that would have been written a moment
// later, once retention frees space. The caller requeues it instead.
func (w *RedisWorker) insertWithRetry(ctx context.Context, spans []repository.Span) (err error, diskFull bool) {
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err = w.repo.InsertSpans(ctx, spans)
		if err == nil {
			return nil, false
		}
		if repository.IsDiskFull(err) {
			return err, true
		}
		w.logger.Info("redis worker: insert attempt failed",
			"attempt", attempt, "count", len(spans), "error", err)

		select {
		case <-ctx.Done():
			return err, false
		case <-time.After(time.Duration(attempt*attempt) * insertRetryBackoff):
		}
	}
	return err, false
}

// requeueAfterDiskFull puts a batch back on the queue instead of dropping it,
// then backs off while retention's emergency eviction reclaims space.
//
// Retention frees space within a cycle and Redis is bounded by its own
// maxmemory, so the backlog is capped either way — but data that sat in the
// queue for a minute is infinitely better than data deleted because the disk
// was briefly full.
func (w *RedisWorker) requeueAfterDiskFull(ctx context.Context, records []model.SpanRecord, count int, cause error) {
	w.logger.Error("redis worker: disk full, returning batch to queue",
		"count", count, "error", cause)

	if err := w.queue.Publish(ctx, records); err != nil {
		w.logger.Error("redis worker: requeue after disk-full failed, batch lost",
			"count", count, "error", err)
		w.metrics.mu.Lock()
		w.metrics.ErrorCount += int64(count)
		w.metrics.mu.Unlock()
	}

	select {
	case <-ctx.Done():
	case <-time.After(diskFullBackoff):
	}
}

func (w *RedisWorker) processBatch(ctx context.Context, records []model.SpanRecord) {
	ctx, span := tracer.Start(ctx, "redis_worker.process_batch")
	defer span.End()
	span.SetAttributes(attribute.Int("batch.size", len(records)))

	spans := convertRecords(records)

	// Staging mode: cheap append to spans_staging and return. The StagingFlusher
	// picks these up per complete trace and does accumulation + classification +
	// indexed storage off the hot path.
	if w.stageOnly {
		w.stageSpans(ctx, spans)
		return
	}

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

	_, insertSpan := tracer.Start(ctx, "redis_worker.insert_spans")
	lastErr, diskFull := w.insertWithRetry(ctx, interesting)
	if lastErr != nil {
		insertSpan.RecordError(lastErr)
		insertSpan.SetStatus(codes.Error, lastErr.Error())
	}
	insertSpan.End()

	if lastErr != nil && diskFull && w.queue != nil {
		span.SetAttributes(attribute.Int("requeued_disk_full", len(interesting)))
		w.requeueAfterDiskFull(ctx, records, len(interesting), lastErr)
		return
	}

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

// stageSpans appends every span to spans_staging with the same bounded retry as
// the inline path. This is the cheap Redis-draining write; the StagingFlusher
// does the expensive classification + indexed storage later.
func (w *RedisWorker) stageSpans(ctx context.Context, spans []repository.Span) {
	ctx, span := tracer.Start(ctx, "redis_worker.stage_spans")
	defer span.End()
	span.SetAttributes(attribute.Int("batch.size", len(spans)))

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := w.repo.InsertSpansStaging(ctx, spans); err != nil {
			lastErr = err
			backoff := time.Duration(attempt*attempt) * 500 * time.Millisecond
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		span.RecordError(lastErr)
		span.SetStatus(codes.Error, lastErr.Error())
		w.logger.Error("redis worker: staging insert failed after retries", "count", len(spans), "error", lastErr)
		w.metrics.mu.Lock()
		w.metrics.ErrorCount += int64(len(spans))
		w.metrics.mu.Unlock()
		return
	}
	w.metrics.mu.Lock()
	w.metrics.ProcessedCount += int64(len(spans))
	w.metrics.mu.Unlock()
}

// classifyForStorage builds the set of spans to write to SQLite.
// All spans in error/slow traces are included unconditionally. Spans in
// verbose-mode projects bypass boring classification. Remaining boring traces
// are sampled whole at the per-project ratio — the die is rolled once per
// trace_id, and either all spans in that trace are kept or none are.
func (w *RedisWorker) classifyForStorage(spans []repository.Span) []repository.Span {
	return classifySpansForStorage(spans, w.cfg.SlowThresholdUs, w.cfg.BoringRetention, w.boringPolicy, w.floor)
}

// classifySpansForStorage builds the set of spans to persist from a set of spans
// that ideally covers whole traces. Extracted so the staging flusher can reuse
// the exact same trace-level classification the inline worker path uses.
func classifySpansForStorage(spans []repository.Span, slowThresholdUs int64, boringRetention time.Duration, boringPolicy BoringPolicyReader, floor *sampling.MinuteFloor) []repository.Span {
	if slowThresholdUs <= 0 {
		return spans
	}

	now := time.Now()

	// stampBoring marks sampled-boring spans with an expires_at so the cleanup can
	// delete them with a plain indexed range scan. A non-positive retention leaves
	// expires_at nil, so those spans fall back to the aggregate-then-delete pass.
	stampBoring := func(bs []repository.Span) {
		if boringRetention <= 0 {
			return
		}
		for i := range bs {
			base := bs[i].IngestedAt
			if base.IsZero() {
				base = now
			}
			exp := base.Add(boringRetention)
			bs[i].ExpiresAt = &exp
		}
	}

	// Determine which projects are in verbose mode (record all spans).
	verboseProject := make(map[int64]bool, 2)
	if boringPolicy != nil {
		for _, s := range spans {
			if _, seen := verboseProject[s.ProjectID]; !seen {
				until := boringPolicy.VerboseUntil(s.ProjectID)
				verboseProject[s.ProjectID] = !until.IsZero() && now.Before(until)
			}
		}
	}

	// First pass: find trace IDs that are definitely interesting.
	interestingTraces := make(map[string]struct{}, len(spans))
	for _, s := range spans {
		if verboseProject[s.ProjectID] || s.Status == "error" || s.DurationUs > slowThresholdUs {
			interestingTraces[s.TraceID] = struct{}{}
		}
	}

	// Second pass: separate interesting from boring, grouping boring by trace.
	result := make([]repository.Span, 0, len(spans))
	// boringTraces maps trace_id → (projectID, spans) for all-or-nothing sampling.
	type boringTrace struct {
		projectID int64
		spans     []repository.Span
	}
	var boringTraceOrder []string
	boringTraceMap := make(map[string]*boringTrace, 8)
	for _, s := range spans {
		if _, ok := interestingTraces[s.TraceID]; ok {
			result = append(result, s)
		} else {
			bt := boringTraceMap[s.TraceID]
			if bt == nil {
				bt = &boringTrace{projectID: s.ProjectID}
				boringTraceMap[s.TraceID] = bt
				boringTraceOrder = append(boringTraceOrder, s.TraceID)
			}
			bt.spans = append(bt.spans, s)
		}
	}

	// Sample whole boring traces per project policy.
	if boringPolicy == nil || len(boringTraceOrder) == 0 {
		return result
	}
	ratioCache := make(map[int64]int, 2)
	minCache := make(map[int64]int, 2)
	for _, traceID := range boringTraceOrder {
		bt := boringTraceMap[traceID]
		ratio, ok := ratioCache[bt.projectID]
		if !ok {
			ratio = boringPolicy.SampleRatio(bt.projectID)
			ratioCache[bt.projectID] = ratio
		}

		// Normal ratio verdict: 1 = keep all, N>1 = keep 1-in-N, 0 = drop.
		ratioKeep := false
		switch {
		case ratio == 1:
			ratioKeep = true
		case ratio > 1:
			ratioKeep = rand.IntN(ratio) == 0
		}

		// The per-(project, operation) minute floor rescues a minimum number of
		// boring traces each minute so quiet operations never vanish entirely —
		// even when ratio == 0 would otherwise drop every boring trace.
		if floor != nil {
			min, ok := minCache[bt.projectID]
			if !ok {
				min = boringPolicy.MinTracesPerMinute(bt.projectID)
				minCache[bt.projectID] = min
			}
			op, minute := boringTraceKey(bt.spans)
			if floor.ShouldKeep(bt.projectID, op, minute, min, ratioKeep) {
				stampBoring(bt.spans)
				result = append(result, bt.spans...)
			}
			continue
		}

		if ratioKeep {
			stampBoring(bt.spans)
			result = append(result, bt.spans...)
		}
	}
	return result
}

// boringTraceKey returns the operation name and wall-clock minute bucket used to
// group a boring trace for the survival floor. It prefers the root span (no
// parent), falling back to the first span in the batch.
func boringTraceKey(spans []repository.Span) (string, int64) {
	root := spans[0]
	for _, s := range spans {
		if s.ParentSpanID == "" {
			root = s
			break
		}
	}
	return root.Name, root.StartTimeUs / 60_000_000
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
