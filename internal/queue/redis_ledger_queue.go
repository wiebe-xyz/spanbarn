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

// SpanLedger returns the span ledger queue, for the single consumer in writer mode.
func (q *RedisQueue) SpanLedger() *BatchQueue[model.SpanLedgerRow] {
	return newBatchQueue[model.SpanLedgerRow](q, SpanLedgerQueueKey, "span-ledger")
}
