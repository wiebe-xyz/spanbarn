package worker

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/sampling"
	"github.com/wiebe-xyz/spanbarn/internal/spanledger"
)

// SpanAccumulator receives every batch of spans for in-memory aggregation.
// Satisfied by *aggregation.Accumulator.
type SpanAccumulator interface {
	AddBatch(spans []repository.Span)
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
	hourFloor    *sampling.MinuteFloor
	logger       *slog.Logger
	metrics      Metrics
	cfg          WorkerConfig
	// stageOnly, when set, makes the worker append every consumed span to the
	// spans_staging table and return immediately, deferring accumulation,
	// classification and indexed storage to the StagingFlusher. This keeps the
	// Redis drain fast (cheap unindexed appends) so the queue never backs up.
	stageOnly bool
	ledger    *spanledger.Tally
	now       func() time.Time
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
	return &RedisWorker{queue: q, repo: repo, logger: logger, now: time.Now}
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

// SetHourFloor wires in the hourly rarity floor: the first clean trace per
// (project, operation, hour) is stored durably. Nil disables the tier.
func (w *RedisWorker) SetHourFloor(f *sampling.MinuteFloor) {
	w.hourFloor = f
}

// Run consumes batches until ctx is cancelled.
func (w *RedisWorker) Run(ctx context.Context) {
	reclaimFrom := w.now().Add(queue.ReclaimDelay)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !w.now().Before(reclaimFrom) {
			w.reclaim(ctx)
		}

		batch, err := w.queue.Consume(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Warn, not error: the connection drop is transient, the backoff
			// below handles it, and a batch whose reply was lost waits on the
			// processing list for reclaim. Logging it at error filed a BugBarn
			// issue for every Redis reconnect.
			w.logger.Warn("redis worker: consume failed, retrying", "error", err)
			// Brief backoff to avoid spinning on a broken Redis connection.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if batch == nil {
			// The block timed out: no items, loop.
			continue
		}

		w.finish(ctx, batch)
	}
}

// finish processes a batch and removes it from the processing list. The removal
// is deferred so a batch that panics leaves too: replayed on every start, it
// would stop span ingest for good.
func (w *RedisWorker) finish(ctx context.Context, batch *queue.SpanBatch) {
	defer w.ack(ctx, batch)
	w.processBatch(ctx, batch.Records)
}

// reclaim returns batches left on the processing list to the queue. Run calls
// it holding no batch, so none of them is this worker's own. The warning is how
// a later gap in the span ledger gets tied to a lost reply: nothing else records
// what the reply held.
func (w *RedisWorker) reclaim(ctx context.Context) {
	got, err := w.queue.Reclaim(ctx)
	if got.Batches > 0 {
		w.logger.Warn("redis worker: reclaimed batches from the processing list, a reply was lost or a writer stopped mid-batch",
			"batches", got.Batches, "spans", got.Spans)
	}
	if err != nil && ctx.Err() == nil {
		w.logger.Warn("redis worker: reclaim failed, retrying", "error", err)
	}
}

// ack removes a finished batch from the processing list. A shutdown does not
// cancel it, or the next writer would replay a batch this one stored.
func (w *RedisWorker) ack(ctx context.Context, batch *queue.SpanBatch) {
	if err := w.queue.Ack(context.WithoutCancel(ctx), batch); err != nil {
		w.logger.Warn("redis worker: ack failed, the batch will replay", "spans", len(batch.Records), "error", err)
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
		// The batch has left the queue, so a shutdown does not cancel its write.
		err = w.repo.InsertSpans(context.WithoutCancel(ctx), spans)
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
		w.ledger.AddSpans(spanledger.Failed, records, 1)
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
		w.accumulator.AddBatch(spans)
	}

	// Classify: interesting spans (error, slow, or verbose-project) are written to
	// SQLite unconditionally; boring spans may be sampled based on per-project policy.
	interesting := w.classifyForStorage(spans)

	boringCount := len(spans) - len(interesting)
	if boringCount > 0 {
		span.SetAttributes(attribute.Int("boring_skipped", boringCount))
	}

	if len(interesting) == 0 {
		w.countProcessed(len(spans))
		w.settleLedger(spans, nil)
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
		w.settleLedger(spans, interesting)
		return
	}

	// Boring spans that classification skipped were handled too: aggregated
	// and deliberately not stored. Counting the whole batch keeps
	// spans_processed_total comparable with staging mode, which counts every
	// span it stages.
	w.countProcessed(len(spans))
	w.settleLedger(spans, nil)

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

	if lastErr := w.stageWithRetry(ctx, spans); lastErr != nil {
		span.RecordError(lastErr)
		span.SetStatus(codes.Error, lastErr.Error())
		w.logger.Error("redis worker: staging insert failed after retries", "count", len(spans), "error", lastErr)
		w.metrics.mu.Lock()
		w.metrics.ErrorCount += int64(len(spans))
		w.metrics.mu.Unlock()
		w.settleLedger(spans, spans)
		return
	}
	w.countProcessed(len(spans))
	w.settleLedger(spans, nil)
}

// stageWithRetry writes the batch with the same quadratic backoff as
// insertWithRetry. The batch has left the queue, so a writer shutting down
// still stores it: a cancelled context stops the retries and leaves the write
// alone. Cancelling the write itself lost the batch with no log and no ledger
// count, at every rollout that caught a write in flight (#478).
func (w *RedisWorker) stageWithRetry(ctx context.Context, spans []repository.Span) error {
	var err error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err = w.repo.InsertSpansStaging(context.WithoutCancel(ctx), spans)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt*attempt) * insertRetryBackoff):
		}
	}
	return err
}

func (w *RedisWorker) countProcessed(n int) {
	w.metrics.mu.Lock()
	w.metrics.ProcessedCount += int64(n)
	w.metrics.mu.Unlock()
}

// Counts returns the spans handled and the spans dropped after retries since
// start. Writer mode exports them as spans_processed_total and
// spans_failed_total.
func (w *RedisWorker) Counts() (processed, failed int64) {
	processed, failed, _ = w.metrics.Snapshot()
	return processed, failed
}
