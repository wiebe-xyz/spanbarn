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
// nil, nil when the queue is empty. BRPOP removes the item before the reply
// reaches the writer, so only a queue that can afford to lose a batch uses it.
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

// processingSuffix names the processing list of a BatchQueue.
const processingSuffix = ":processing"

// Batch is one consumed queue item. It stays on the processing list until Ack
// removes it.
type Batch[T any] struct {
	Records []T
	raw     string
}

// BatchQueue consumes one queue the way Consume does the span queues: a batch
// stays on a processing list until it is stored, so a reply lost on the way to
// the writer or a writer killed mid-write leaves it recoverable.
//
// Delivery is at least once. A lost reply replays a batch nothing has stored, so
// it duplicates nothing. A writer killed between the store and the Ack, or an Ack
// that fails, replays a stored batch. These queues carry no key to catch that, so
// the replay stores it twice; see the callers for what that costs on each.
type BatchQueue[T any] struct {
	q             *RedisQueue
	key           string
	processingKey string
	label         string
}

func newBatchQueue[T any](q *RedisQueue, key, label string) *BatchQueue[T] {
	return &BatchQueue[T]{q: q, key: key, processingKey: key + processingSuffix, label: label}
}

// Consume moves one batch to the processing list and returns it, blocking for up
// to brpopTimeout. Returns nil, nil when the queue is empty.
func (b *BatchQueue[T]) Consume(ctx context.Context) (*Batch[T], error) {
	raw, err := b.q.client.BLMove(ctx, b.key, b.processingKey, "RIGHT", "LEFT", brpopTimeout).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: claim %s: %w", b.label, err)
	}
	records, err := decodeClaim[T](ctx, b.q, b.processingKey, raw)
	if err != nil {
		return nil, fmt.Errorf("queue: %s: %w", b.label, err)
	}
	return &Batch[T]{Records: records, raw: raw}, nil
}

// Ack removes a batch from the processing list.
func (b *BatchQueue[T]) Ack(ctx context.Context, batch *Batch[T]) error {
	return b.q.ack(ctx, b.processingKey, batch.raw)
}

// Reclaim moves every batch on the processing list to the front of the queue. The
// consumer calls it holding no batch of its own.
func (b *BatchQueue[T]) Reclaim(ctx context.Context) (Reclaimed, error) {
	return b.q.reclaim(ctx, b.processingKey, b.key)
}

// decodeClaim parses a claimed item. An item that does not parse cannot parse on
// a replay either, so it leaves the processing list here.
func decodeClaim[T any](ctx context.Context, q *RedisQueue, processingKey, raw string) ([]T, error) {
	var records []T
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		if ackErr := q.client.LRem(ctx, processingKey, 1, raw).Err(); ackErr != nil {
			return nil, fmt.Errorf("unmarshal: %w (and lrem: %v)", err, ackErr)
		}
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return records, nil
}

func (q *RedisQueue) ack(ctx context.Context, processingKey, raw string) error {
	if err := q.client.LRem(ctx, processingKey, 1, raw).Err(); err != nil {
		return fmt.Errorf("queue: lrem: %w", err)
	}
	return nil
}

// Reclaimed reports what a reclaim moved back.
type Reclaimed struct {
	Batches int
	Records int
}

// reclaim moves every batch on a processing list to the end of queueKey that the
// next consume reads. Whatever is on the list was left by a lost reply, a failed
// Ack or a writer that stopped mid-batch. A failure partway leaves the rest on
// the list for the next call; the move loses nothing.
func (q *RedisQueue) reclaim(ctx context.Context, processingKey, queueKey string) (Reclaimed, error) {
	var out Reclaimed
	for {
		raw, err := q.client.LMove(ctx, processingKey, queueKey, "RIGHT", "RIGHT").Result()
		if err == redis.Nil {
			return out, nil
		}
		if err != nil {
			return out, fmt.Errorf("queue: reclaim: %w", err)
		}
		out.Batches++
		// Only the count is read. A batch that does not parse counts no records,
		// and the consume drops it with an error.
		var records []json.RawMessage
		if json.Unmarshal([]byte(raw), &records) == nil {
			out.Records += len(records)
		}
	}
}
