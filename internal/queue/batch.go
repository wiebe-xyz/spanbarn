package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// publishBatch marshals records to JSON and LPUSHes them to key in batches of
// batchSize. label names the record kind in error messages. Shared by the
// per-type Publish* methods, which otherwise differ only in record type, queue
// key, batch size and label.
func publishBatch[T any](ctx context.Context, q *RedisQueue, key, label string, batchSize int, records []T) error {
	for i := 0; i < len(records); i += batchSize {
		end := i + batchSize
		if end > len(records) {
			end = len(records)
		}
		data, err := json.Marshal(records[i:end])
		if err != nil {
			return fmt.Errorf("queue: marshal %s: %w", label, err)
		}
		if err := q.client.LPush(ctx, key, data).Err(); err != nil {
			return fmt.Errorf("queue: lpush %s: %w", label, err)
		}
	}
	return nil
}

// consumeBatch blocks up to brpopTimeout for one batch from key, returning
// nil, nil when the queue is empty.
func consumeBatch[T any](ctx context.Context, q *RedisQueue, key, label string) ([]T, error) {
	result, err := q.client.BRPop(ctx, brpopTimeout, key).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: brpop %s: %w", label, err)
	}
	var records []T
	if err := json.Unmarshal([]byte(result[1]), &records); err != nil {
		return nil, fmt.Errorf("queue: unmarshal %s: %w", label, err)
	}
	return records, nil
}
