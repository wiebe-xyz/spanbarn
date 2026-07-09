package queue

import (
	"context"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	LogsQueueKey       = "spanbarn:logs-queue"
	logsQueueBatchSize = 500
)

// PublishLogs serialises log records as JSON and LPUSHes them to the logs queue
// in batches. Called by the reader pod's LogsHandler.
func (q *RedisQueue) PublishLogs(ctx context.Context, records []model.LogRecord) error {
	return publishBatch(ctx, q, LogsQueueKey, "logs", logsQueueBatchSize, records)
}

// ConsumeLogs blocks for up to brpopTimeout waiting for a batch from the logs
// queue. Returns nil, nil when the queue is empty.
// Called by the writer pod's logs consumer goroutine.
func (q *RedisQueue) ConsumeLogs(ctx context.Context) ([]model.LogRecord, error) {
	return consumeBatch[model.LogRecord](ctx, q, LogsQueueKey, "logs")
}

// LogsPublisher implements ingest.LogsRepository by publishing records to the
// Redis logs queue. Used in reader mode so logs flow through the same Redis
// hand-off as spans and metrics.
type LogsPublisher struct {
	queue *RedisQueue
}

// NewLogsPublisher wraps a RedisQueue as a LogsRepository.
func NewLogsPublisher(q *RedisQueue) *LogsPublisher {
	return &LogsPublisher{queue: q}
}

// InsertLogs publishes records to Redis. The writer pod consumes and stores them.
func (p *LogsPublisher) InsertLogs(ctx context.Context, recs []model.LogRecord) error {
	return p.queue.PublishLogs(ctx, recs)
}
