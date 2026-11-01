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
// repo is the raw insert, usually behind a MetricRepeatFilter.
//
// A failed raw insert still finishes the batch, because the accumulator has
// counted it and a replay would add its sums and counts again. A writer killed
// after the raw insert does replay the batch. If the accumulator flushed those
// points first, that bucket reads high by one batch.
func runMetricsConsumer(ctx context.Context, q batchSource[model.MetricRecord], acc *aggregation.MetricAccumulator, repo aggregation.MetricWriter, logger *slog.Logger) {
	newBatchConsumer("metrics", q, func(ctx context.Context, recs []model.MetricRecord) bool {
		for i := range recs {
			acc.AddMetric(recs[i])
		}
		if err := repo.InsertMetrics(ctx, recs); err != nil {
			logger.Error("metrics insert error", "error", err)
		}
		return true
	}, logger).run(ctx)
}

// runLogsConsumer drains the logs queue into the logs table until ctx is
// cancelled. A failed insert is logged and the batch finished. A writer killed
// after the insert replays the batch, and the logs table has no key to catch the
// repeated lines.
func runLogsConsumer(ctx context.Context, q batchSource[model.LogRecord], repo logsSink, logger *slog.Logger) {
	newBatchConsumer("logs", q, func(ctx context.Context, recs []model.LogRecord) bool {
		if err := repo.InsertLogs(ctx, recs); err != nil {
			logger.Error("logs insert error", "error", err)
		}
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
