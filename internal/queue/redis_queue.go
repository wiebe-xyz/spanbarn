package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	WriteQueueKey     = "spanbarn:write-queue"
	maxRecordsPerItem = 500
	brpopTimeout      = 5 * time.Second
)

// RedisQueue is a durable write queue backed by a Redis list.
// Producers call Publish (LPUSH); the single consumer calls Consume (BRPOP).
type RedisQueue struct {
	client *redis.Client
}

// NewRedisQueue connects to Redis at redisURL and verifies connectivity.
func NewRedisQueue(redisURL string) (*RedisQueue, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("queue: parse redis url: %w", err)
	}
	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("queue: redis ping: %w", err)
	}

	return &RedisQueue{client: client}, nil
}

// Publish serialises records as JSON and LPUSHes them to the queue in batches
// of up to maxRecordsPerItem. Called by the RedisForwarder in reader mode.
func (q *RedisQueue) Publish(ctx context.Context, records []model.SpanRecord) error {
	for i := 0; i < len(records); i += maxRecordsPerItem {
		end := i + maxRecordsPerItem
		if end > len(records) {
			end = len(records)
		}
		data, err := json.Marshal(records[i:end])
		if err != nil {
			return fmt.Errorf("queue: marshal: %w", err)
		}
		if err := q.client.LPush(ctx, WriteQueueKey, data).Err(); err != nil {
			return fmt.Errorf("queue: lpush: %w", err)
		}
	}
	return nil
}

// Consume blocks for up to brpopTimeout waiting for a batch, then returns it.
// Returns nil, nil when the timeout expires with no items — callers should loop.
// Called by the RedisWorker in writer mode.
func (q *RedisQueue) Consume(ctx context.Context) ([]model.SpanRecord, error) {
	result, err := q.client.BRPop(ctx, brpopTimeout, WriteQueueKey).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: brpop: %w", err)
	}
	// result[0] = key, result[1] = JSON payload
	var records []model.SpanRecord
	if err := json.Unmarshal([]byte(result[1]), &records); err != nil {
		return nil, fmt.Errorf("queue: unmarshal: %w", err)
	}
	return records, nil
}

// Len returns the current depth of the write queue. Used for health metrics.
func (q *RedisQueue) Len(ctx context.Context) (int64, error) {
	n, err := q.client.LLen(ctx, WriteQueueKey).Result()
	if err != nil {
		return 0, fmt.Errorf("queue: llen: %w", err)
	}
	return n, nil
}

// Close releases the underlying Redis connection.
func (q *RedisQueue) Close() error {
	return q.client.Close()
}
