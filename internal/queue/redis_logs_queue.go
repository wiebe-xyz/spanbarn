package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	LogsQueueKey       = "spanbarn:logs-queue"
	logsQueueBatchSize = 500
)

// PublishLogs serialises log records as JSON and LPUSHes them to the logs queue
// in batches. Called by the reader pod's LogsHandler.
func (q *RedisQueue) PublishLogs(ctx context.Context, records []model.LogRecord) error {
	for i := 0; i < len(records); i += logsQueueBatchSize {
		end := i + logsQueueBatchSize
		if end > len(records) {
			end = len(records)
		}
		data, err := json.Marshal(records[i:end])
		if err != nil {
			return fmt.Errorf("queue: marshal logs: %w", err)
		}
		if err := q.client.LPush(ctx, LogsQueueKey, data).Err(); err != nil {
			return fmt.Errorf("queue: lpush logs: %w", err)
		}
	}
	return nil
}

// ConsumeLogs blocks for up to brpopTimeout waiting for a batch from the logs
// queue. Returns nil, nil when the queue is empty.
// Called by the writer pod's logs consumer goroutine.
func (q *RedisQueue) ConsumeLogs(ctx context.Context) ([]model.LogRecord, error) {
	result, err := q.client.BRPop(ctx, brpopTimeout, LogsQueueKey).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: brpop logs: %w", err)
	}
	var records []model.LogRecord
	if err := json.Unmarshal([]byte(result[1]), &records); err != nil {
		return nil, fmt.Errorf("queue: unmarshal logs: %w", err)
	}
	return records, nil
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
