package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/sampling"
	"github.com/wiebe-xyz/spanbarn/internal/worker"
)

type logsSink interface {
	InsertLogs(ctx context.Context, recs []model.LogRecord) error
}

// batchSource is one queue's consume side.
type batchSource[T any] interface {
	Consume(ctx context.Context) (*queue.Batch[T], error)
	Ack(ctx context.Context, batch *queue.Batch[T]) error
	Reclaim(ctx context.Context) (queue.Reclaimed, error)
}

// batchConsumer drains one queue until its context is cancelled. A consumed
// batch stays on the queue's processing list until it is acked, so a lost reply
// or a killed writer leaves it for the reclaim.
//
// store runs with a context that shutdown does not cancel, because a cancelled
// insert would drop the batch. The write schedulers run until the consumers have
// returned. It reports whether the batch is finished with; false leaves it on
// the processing list for the reclaim to return.
type batchConsumer[T any] struct {
	name   string
	source batchSource[T]
	store  func(ctx context.Context, records []T) bool
	logger *slog.Logger
	now    func() time.Time
}

func newBatchConsumer[T any](name string, source batchSource[T], store func(context.Context, []T) bool, logger *slog.Logger) *batchConsumer[T] {
	return &batchConsumer[T]{name: name, source: source, store: store, logger: logger, now: time.Now}
}

func (c *batchConsumer[T]) run(ctx context.Context) {
	reclaimFrom := c.now().Add(queue.ReclaimDelay)
	for ctx.Err() == nil {
		if !c.now().Before(reclaimFrom) {
			c.reclaim(ctx)
		}
		batch, err := c.source.Consume(ctx)
		if err != nil {
			if !backoffAfterConsumeError(ctx, c.logger, c.name, err) {
				return
			}
			continue
		}
		if batch != nil {
			c.process(ctx, batch)
		}
	}
}

// process acks on a panic too: a batch that panics the store would panic every
// replay.
func (c *batchConsumer[T]) process(ctx context.Context, batch *queue.Batch[T]) {
	finished := true
	defer func() {
		if finished {
			c.ack(ctx, batch)
		}
	}()
	finished = c.store(context.WithoutCancel(ctx), batch.Records)
}

// ack removes a finished batch from the processing list. A shutdown does not
// cancel it, or the next writer would replay a batch this one stored.
func (c *batchConsumer[T]) ack(ctx context.Context, batch *queue.Batch[T]) {
	if err := c.source.Ack(context.WithoutCancel(ctx), batch); err != nil {
		c.logger.Warn(c.name+" consumer: ack failed, the batch will replay", "records", len(batch.Records), "error", err)
	}
}

// reclaim returns batches left on the processing list to the queue. run calls it
// holding no batch, so none of them is its own. The warning is how a doubled
// count or a repeated log line gets tied to a replay: a replay of a stored batch
// looks the same as new data once it is stored.
func (c *batchConsumer[T]) reclaim(ctx context.Context) {
	got, err := c.source.Reclaim(ctx)
	if got.Batches > 0 {
		c.logger.Warn(c.name+" consumer: reclaimed batches from the processing list, a reply was lost or a writer stopped mid-batch",
			"batches", got.Batches, "records", got.Records)
	}
	if err != nil && ctx.Err() == nil {
		c.logger.Warn(c.name+" consumer: reclaim failed, retrying", "error", err)
	}
}

// runMetricsConsumer drains the metrics queue into the rollup accumulator and
// the metrics table until ctx is cancelled. The accumulator sees every point;
// raw is the raw insert behind the repeat filter.
//
// The accumulator counts a batch before its raw insert, so only the raw insert
// is retried, and the batch is finished even when the retries fail: a replay
// would add its sums and counts again. The filter has then recorded the dropped
// points as written, so an unchanged stream writes its next raw point at the
// next heartbeat. A writer killed after the raw insert does replay the batch. If
// the accumulator flushed those points first, that bucket reads high by one
// batch.
func runMetricsConsumer(ctx context.Context, q batchSource[model.MetricRecord], acc *aggregation.MetricAccumulator, raw *aggregation.FilteredMetricWriter, logger *slog.Logger) {
	newBatchConsumer("metrics", q, func(_ context.Context, recs []model.MetricRecord) bool {
		for i := range recs {
			acc.AddMetric(recs[i])
		}
		kept := raw.Keep(recs)
		err := storeWithRetry(ctx, logger, "metrics", len(kept), func(sctx context.Context) error {
			return raw.Write(sctx, kept)
		})
		if err != nil {
			logger.Error("metrics insert failed, raw points dropped", "points", len(kept), "error", err)
		}
		return true
	}, logger).run(ctx)
}

// runLogsConsumer drains the logs queue into the logs table until ctx is
// cancelled. The insert is one transaction, so a failed one stored nothing and
// is retried. A shutdown during the retries leaves the batch on the processing
// list for the next writer. A batch that still fails after storeAttempts is
// dropped with an error, so it does not hold up the queue behind it. A writer
// killed after the insert replays the batch, and the logs table has no key to
// catch the repeated lines.
func runLogsConsumer(ctx context.Context, q batchSource[model.LogRecord], repo logsSink, logger *slog.Logger) {
	newBatchConsumer("logs", q, func(_ context.Context, recs []model.LogRecord) bool {
		err := storeWithRetry(ctx, logger, "logs", len(recs), func(sctx context.Context) error {
			return repo.InsertLogs(sctx, recs)
		})
		switch {
		case err == nil:
			return true
		case ctx.Err() != nil:
			logger.Warn("logs insert failed at shutdown, leaving the batch for the next writer", "records", len(recs), "error", err)
			return false
		}
		logger.Error("logs insert failed after retries, batch dropped", "records", len(recs), "attempts", storeAttempts, "error", err)
		return true
	}, logger).run(ctx)
}

// newStagingFlusher builds the span staging flusher (opt-in): the redis worker
// only appends to spans_staging; the flusher does accumulation, classification
// and indexed storage per complete trace off the hot path, with a hard-age GC so
// staging can't grow unbounded.
func newStagingFlusher(cfg config.Config, queryRepo, repo *repository.Repository, acc *aggregation.Accumulator, policy worker.BoringPolicyReader, floor, hourFloor *sampling.MinuteFloor, logger *slog.Logger) *worker.StagingFlusher {
	flusher := worker.NewStagingFlusher(queryRepo, repo, worker.StagingFlusherConfig{
		Window:          time.Duration(cfg.TraceBufferWindowSeconds) * time.Second,
		MaxAge:          time.Duration(cfg.StagingMaxAgeSeconds) * time.Second,
		SlowThresholdUs: int64(cfg.SlowThresholdMS) * 1000,
		BoringRetention: time.Duration(cfg.Retention.BoringMinutes) * time.Minute,
	}, logger)
	flusher.SetAccumulator(acc)
	flusher.SetBoringPolicy(policy)
	flusher.SetMinuteFloor(floor)
	flusher.SetHourFloor(hourFloor)
	logger.Info("span staging enabled: worker stages spans, flusher classifies per trace",
		"window_s", cfg.TraceBufferWindowSeconds, "max_age_s", cfg.StagingMaxAgeSeconds)
	return flusher
}

// filteredRawMetrics is repo's raw metrics insert behind the repeat filter.
func filteredRawMetrics(cfg config.Config, repo *repository.Repository) *aggregation.FilteredMetricWriter {
	heartbeat := time.Duration(cfg.MetricRepeatHeartbeatSeconds) * time.Second
	return aggregation.NewFilteredMetricWriter(repo, aggregation.NewMetricRepeatFilter(heartbeat))
}
