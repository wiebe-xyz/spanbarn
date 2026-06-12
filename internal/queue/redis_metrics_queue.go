package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	MetricsQueueKey       = "spanbarn:metrics-queue"
	metricsQueueBatchSize = 500
)

// PublishMetrics serialises metric records as JSON and LPUSHes them to the
// metrics queue in batches. Called by the reader pod's MetricsHandler.
func (q *RedisQueue) PublishMetrics(ctx context.Context, records []model.MetricRecord) error {
	for i := 0; i < len(records); i += metricsQueueBatchSize {
		end := i + metricsQueueBatchSize
		if end > len(records) {
			end = len(records)
		}
		data, err := json.Marshal(records[i:end])
		if err != nil {
			return fmt.Errorf("queue: marshal metrics: %w", err)
		}
		if err := q.client.LPush(ctx, MetricsQueueKey, data).Err(); err != nil {
			return fmt.Errorf("queue: lpush metrics: %w", err)
		}
	}
	return nil
}

// ConsumeMetrics blocks for up to brpopTimeout waiting for a batch from the
// metrics queue. Returns nil, nil when the queue is empty.
// Called by the writer pod's metrics consumer goroutine.
func (q *RedisQueue) ConsumeMetrics(ctx context.Context) ([]model.MetricRecord, error) {
	result, err := q.client.BRPop(ctx, brpopTimeout, MetricsQueueKey).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: brpop metrics: %w", err)
	}
	var records []model.MetricRecord
	if err := json.Unmarshal([]byte(result[1]), &records); err != nil {
		return nil, fmt.Errorf("queue: unmarshal metrics: %w", err)
	}
	return records, nil
}

// MetricsPublisher implements ingest.MetricsRepository by publishing records to
// the Redis metrics queue. Used in reader mode so metrics flow through the same
// Redis hand-off as spans.
type MetricsPublisher struct {
	queue *RedisQueue
}

// NewMetricsPublisher wraps a RedisQueue as a MetricsRepository.
func NewMetricsPublisher(q *RedisQueue) *MetricsPublisher {
	return &MetricsPublisher{queue: q}
}

// InsertMetrics publishes records to Redis. The writer pod consumes and stores them.
func (p *MetricsPublisher) InsertMetrics(ctx context.Context, recs []model.MetricRecord) error {
	return p.queue.PublishMetrics(ctx, recs)
}
