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
	// WriteQueueRecentKey holds batches whose spans have start_time_us within
	// RecentThreshold. The consumer drains this before the backlog queue.
	WriteQueueRecentKey = "spanbarn:write-queue:recent"
	// WriteQueueBacklogKey is the legacy key and now serves as the backlog queue
	// for spans older than RecentThreshold.
	WriteQueueBacklogKey = "spanbarn:write-queue"
	// WriteQueueKey is kept as an alias of WriteQueueBacklogKey so that any
	// external tooling referencing it still works.
	WriteQueueKey     = WriteQueueBacklogKey
	maxRecordsPerItem = 500
	brpopTimeout      = 5 * time.Second
	// RecentThreshold is the max age of start_time_us for a batch to be routed
	// to the recent (high-priority) queue.
	RecentThreshold = 5 * time.Minute
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

// NewRedisQueueWithRetry connects to Redis at redisURL, retrying with backoff
// until ctx is cancelled or the connection succeeds. Used in writer mode where
// the Redis queue pod may start after the writer pod during a rolling deploy.
func NewRedisQueueWithRetry(ctx context.Context, redisURL string) (*RedisQueue, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("queue: parse redis url: %w", err)
	}

	backoff := time.Second
	for {
		client := redis.NewClient(opts)
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := client.Ping(pingCtx).Err()
		cancel()
		if err == nil {
			return &RedisQueue{client: client}, nil
		}
		client.Close()

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("queue: context cancelled waiting for redis: %w", ctx.Err())
		case <-time.After(backoff):
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
}

// Publish serialises records as JSON and LPUSHes them to the appropriate queue
// in batches of up to maxRecordsPerItem. Batches whose spans have a max
// start_time_us within RecentThreshold go to the recent (high-priority) queue;
// older batches go to the backlog queue. The consumer drains recent first.
func (q *RedisQueue) Publish(ctx context.Context, records []model.SpanRecord) error {
	for i := 0; i < len(records); i += maxRecordsPerItem {
		end := i + maxRecordsPerItem
		if end > len(records) {
			end = len(records)
		}
		chunk := records[i:end]

		var maxStart int64
		for _, r := range chunk {
			if r.StartTimeUs > maxStart {
				maxStart = r.StartTimeUs
			}
		}
		key := WriteQueueBacklogKey
		if maxStart > 0 && time.Since(time.UnixMicro(maxStart)) < RecentThreshold {
			key = WriteQueueRecentKey
		}

		data, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("queue: marshal: %w", err)
		}
		if err := q.client.LPush(ctx, key, data).Err(); err != nil {
			return fmt.Errorf("queue: lpush: %w", err)
		}
	}
	return nil
}

// Consume blocks for up to brpopTimeout waiting for a batch from either queue.
// The recent (high-priority) queue is checked first; if empty, the backlog queue
// is checked. Returns nil, nil when both queues are empty.
// Called by the RedisWorker in writer mode.
func (q *RedisQueue) Consume(ctx context.Context) ([]model.SpanRecord, error) {
	result, err := q.client.BRPop(ctx, brpopTimeout, WriteQueueRecentKey, WriteQueueBacklogKey).Result()
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

// Len returns the total depth of both write queues. Used for health metrics.
func (q *RedisQueue) Len(ctx context.Context) (int64, error) {
	recent, err := q.client.LLen(ctx, WriteQueueRecentKey).Result()
	if err != nil {
		return 0, fmt.Errorf("queue: llen recent: %w", err)
	}
	backlog, err := q.client.LLen(ctx, WriteQueueBacklogKey).Result()
	if err != nil {
		return 0, fmt.Errorf("queue: llen backlog: %w", err)
	}
	return recent + backlog, nil
}

// Depths returns the per-queue backlog (spans recent+backlog, metrics, logs),
// used for self-metrics. A failed LLEN leaves that label absent.
func (q *RedisQueue) Depths(ctx context.Context) map[string]int64 {
	out := map[string]int64{}
	add := func(label string, keys ...string) {
		var total int64
		for _, k := range keys {
			n, err := q.client.LLen(ctx, k).Result()
			if err != nil {
				return
			}
			total += n
		}
		out[label] = total
	}
	add("spans", WriteQueueRecentKey, WriteQueueBacklogKey)
	add("metrics", MetricsQueueKey)
	add("logs", LogsQueueKey)
	return out
}

// Close releases the underlying Redis connection.
func (q *RedisQueue) Close() error {
	return q.client.Close()
}
