package queue

import (
	"context"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	// SpanLedgerQueueKey carries the span ledger counts of pods that cannot
	// write to the writer, which stores them.
	SpanLedgerQueueKey       = "spanbarn:span-ledger-queue"
	spanLedgerQueueBatchSize = 500
)

// PublishSpanLedger LPUSHes span ledger rows onto the ledger queue.
func (q *RedisQueue) PublishSpanLedger(ctx context.Context, rows []model.SpanLedgerRow) error {
	return publishBatch(ctx, q, SpanLedgerQueueKey, "span-ledger", spanLedgerQueueBatchSize, rows)
}

// ConsumeSpanLedger blocks for up to brpopTimeout waiting for a batch of ledger
// rows. Returns nil, nil when the queue is empty.
func (q *RedisQueue) ConsumeSpanLedger(ctx context.Context) ([]model.SpanLedgerRow, error) {
	return consumeBatch[model.SpanLedgerRow](ctx, q, SpanLedgerQueueKey, "span-ledger")
}
