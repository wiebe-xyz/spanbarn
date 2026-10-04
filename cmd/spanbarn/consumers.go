package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/sampling"
	"github.com/wiebe-xyz/spanbarn/internal/worker"
)

// runMetricsConsumer drains the metrics queue into the rollup accumulator and
// the metrics table until ctx is cancelled.
func runMetricsConsumer(ctx context.Context, q *queue.RedisQueue, acc *aggregation.MetricAccumulator, repo *repository.Repository, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		recs, err := q.ConsumeMetrics(ctx)
		if err != nil {
			if !backoffAfterConsumeError(ctx, logger, "metrics", err) {
				return
			}
			continue
		}
		if len(recs) == 0 {
			continue
		}
		for i := range recs {
			acc.AddMetric(recs[i])
		}
		if err := repo.InsertMetrics(ctx, recs); err != nil {
			logger.Error("metrics insert error", "error", err)
		}
	}
}

// runLogsConsumer drains the logs queue into the logs table until ctx is
// cancelled.
func runLogsConsumer(ctx context.Context, q *queue.RedisQueue, repo *repository.Repository, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		recs, err := q.ConsumeLogs(ctx)
		if err != nil {
			if !backoffAfterConsumeError(ctx, logger, "logs", err) {
				return
			}
			continue
		}
		if len(recs) == 0 {
			continue
		}
		if err := repo.InsertLogs(ctx, recs); err != nil {
			logger.Error("logs insert error", "error", err)
		}
	}
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
