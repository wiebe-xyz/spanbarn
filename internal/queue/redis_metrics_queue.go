package queue

import (
	"context"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	MetricsQueueKey       = "spanbarn:metrics-queue"
	metricsQueueBatchSize = 500
)

// PublishMetrics serialises metric records as JSON and LPUSHes them to the
// metrics queue in batches. Called by the reader pod's MetricsHandler.
func (q *RedisQueue) PublishMetrics(ctx context.Context, records []model.MetricRecord) error {
	return publishBatch(ctx, q, MetricsQueueKey, "metrics", metricsQueueBatchSize, records)
}

// ConsumeMetrics blocks for up to brpopTimeout waiting for a batch from the
// metrics queue. Returns nil, nil when the queue is empty.
// Called by the writer pod's metrics consumer goroutine.
func (q *RedisQueue) ConsumeMetrics(ctx context.Context) ([]model.MetricRecord, error) {
	return consumeBatch[model.MetricRecord](ctx, q, MetricsQueueKey, "metrics")
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
