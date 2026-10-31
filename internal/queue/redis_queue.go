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
	WriteQueueKey = WriteQueueBacklogKey
	// WriteQueueProcessingKey holds the batch the writer is working on. A batch
	// stays here until it is stored, so a reply lost on the way to the writer or
	// a writer killed mid-write leaves it recoverable.
	WriteQueueProcessingKey = "spanbarn:write-queue:processing"
	maxRecordsPerItem       = 500
	brpopTimeout            = 5 * time.Second
	// ReclaimDelay is how long a writer leaves the processing list alone after
	// it starts. A writer pod normally replaces the old one only after it has
	// gone (Recreate), but a deleted or evicted pod is replaced at once while
	// the old one drains its last batch for up to terminationGracePeriodSeconds.
	// The delay outlasts that grace period, so the batch left on the list is one
	// nobody is working on. deploy/k8s/graceful_stop_test.go holds it above the
	// grace period.
	ReclaimDelay = 2 * time.Minute
	// RecentThreshold is the max age of start_time_us for a batch to be routed
	// to the recent (high-priority) queue.
	RecentThreshold = 5 * time.Minute
)

// RedisQueue is a durable write queue backed by a Redis list.
// Producers call Publish (LPUSH); the single consumer calls Consume (LMOVE) and Ack.
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

// SpanBatch is one consumed queue item. It stays on the processing list until
// Ack removes it.
type SpanBatch struct {
	Records []model.SpanRecord
	raw     string
}

// Consume moves one batch to the processing list and returns it, blocking for
// up to brpopTimeout when both queues are empty. The recent (high-priority)
// queue is checked first; if empty, the backlog queue is checked. Returns nil,
// nil when both queues are empty.
//
// BRPOP removed the item on the server before the reply reached the writer, so
// a read timeout during a stalled write lost the batch. The move keeps a copy
// until Ack, so a lost reply delays the batch and Reclaim returns it.
//
// Called by the RedisWorker in writer mode.
func (q *RedisQueue) Consume(ctx context.Context) (*SpanBatch, error) {
	raw, err := q.claim(ctx)
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: claim: %w", err)
	}
	var records []model.SpanRecord
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		// It cannot parse on a replay either, so it leaves the list here.
		if ackErr := q.client.LRem(ctx, WriteQueueProcessingKey, 1, raw).Err(); ackErr != nil {
			return nil, fmt.Errorf("queue: unmarshal: %w (and lrem: %v)", err, ackErr)
		}
		return nil, fmt.Errorf("queue: unmarshal: %w", err)
	}
	return &SpanBatch{Records: records, raw: raw}, nil
}

// claim moves the next batch to the processing list. BLMOVE takes one source,
// so it blocks on the recent queue only: a batch landing on an empty backlog
// waits for that block to end, up to brpopTimeout.
func (q *RedisQueue) claim(ctx context.Context) (string, error) {
	for _, key := range []string{WriteQueueRecentKey, WriteQueueBacklogKey} {
		raw, err := q.client.LMove(ctx, key, WriteQueueProcessingKey, "RIGHT", "LEFT").Result()
		if err != redis.Nil {
			return raw, err
		}
	}
	return q.client.BLMove(ctx, WriteQueueRecentKey, WriteQueueProcessingKey, "RIGHT", "LEFT", brpopTimeout).Result()
}

// Ack removes a batch from the processing list once it is stored or counted as
// failed.
func (q *RedisQueue) Ack(ctx context.Context, b *SpanBatch) error {
	if err := q.client.LRem(ctx, WriteQueueProcessingKey, 1, b.raw).Err(); err != nil {
		return fmt.Errorf("queue: lrem: %w", err)
	}
	return nil
}

// Reclaimed reports what Reclaim moved back.
type Reclaimed struct {
	Batches int
	Spans   int
}

// Reclaim moves every batch on the processing list to the front of the recent
// queue, where the next Consume finds it. The consumer calls it holding no batch
// of its own, so whatever is on the list was left by a lost reply, a failed Ack
// or a writer that stopped mid-batch. A failure partway leaves the rest on the
// list for the next call; the move loses nothing.
func (q *RedisQueue) Reclaim(ctx context.Context) (Reclaimed, error) {
	var out Reclaimed
	for {
		raw, err := q.client.LMove(ctx, WriteQueueProcessingKey, WriteQueueRecentKey, "RIGHT", "RIGHT").Result()
		if err == redis.Nil {
			return out, nil
		}
		if err != nil {
			return out, fmt.Errorf("queue: reclaim: %w", err)
		}
		out.Batches++
		// Only the count is read. A batch that does not parse counts no spans,
		// and Consume drops it with an error.
		var records []json.RawMessage
		if json.Unmarshal([]byte(raw), &records) == nil {
			out.Spans += len(records)
		}
	}
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
