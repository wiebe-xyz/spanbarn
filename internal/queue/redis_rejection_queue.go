package queue

import (
	"context"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	// KeyRejectionQueueKey carries the rejected-key counts of pods that cannot
	// write to the writer, which adds them to key_rejections (#572).
	KeyRejectionQueueKey       = "spanbarn:key-rejection-queue"
	keyRejectionQueueBatchSize = 500
)

// PublishKeyRejections LPUSHes rejected-key counts onto the rejection queue.
func (q *RedisQueue) PublishKeyRejections(ctx context.Context, rows []model.KeyRejection) error {
	return publishBatch(ctx, q, KeyRejectionQueueKey, "key-rejection", keyRejectionQueueBatchSize, rows)
}

// ConsumeKeyRejections blocks for up to brpopTimeout for a batch of counts and
// returns nil, nil when the queue is empty. The queue keeps BRPOP: a lost batch
// is a few counts of a diagnostic tally, and a replay would double them.
func (q *RedisQueue) ConsumeKeyRejections(ctx context.Context) ([]model.KeyRejection, error) {
	return consumeBatch[model.KeyRejection](ctx, q, KeyRejectionQueueKey, "key-rejection")
}
