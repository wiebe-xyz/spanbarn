package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanledger"
)

const (
	// spanLedgerFinalReportTimeout bounds the last report of a stopping pod,
	// which has to finish inside the termination grace period.
	spanLedgerFinalReportTimeout = 5 * time.Second

	// spanLedgerRetryWait keeps the consumer from spinning on a batch it cannot
	// store or a queue it cannot read.
	spanLedgerRetryWait = 5 * time.Second
)

// startSpanLedgerReports reports the tally's counts until the returned func is
// called, which stops the reports and makes the last one. Call it once the work
// the tally counts has stopped. The reports run on their own context, so a pod
// that has had SIGTERM keeps reporting through its drain.
func startSpanLedgerReports(tally *spanledger.Tally, sink spanledger.Sink, logger *slog.Logger) (finish func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	safeGo("span-ledger-reports", &wg, func() { tally.Run(ctx, spanledger.ReportInterval, sink, logger) })
	return func() {
		cancel()
		wg.Wait()
		finalCtx, done := context.WithTimeout(context.Background(), spanLedgerFinalReportTimeout)
		defer done()
		if err := tally.Report(finalCtx, sink); err != nil {
			logger.Warn("span ledger: last report failed, its spans will read as lost", "error", err)
		}
	}
}

// runSpanLedgerConsumer stores the ledger rows reader pods publish. A batch it
// cannot store goes back on the queue, because a missing exit count reads as
// loss.
func runSpanLedgerConsumer(ctx context.Context, writeQueue *queue.RedisQueue, repo *repository.Repository, logger *slog.Logger) {
	for ctx.Err() == nil {
		rows, err := writeQueue.ConsumeSpanLedger(ctx)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("span ledger consumer error", "error", err)
				waitSpanLedgerRetry(ctx)
			}
			continue
		}
		if err := repo.AddSpanLedgerRows(rows); err != nil {
			logger.Warn("span ledger: store failed, returning rows to the queue", "rows", len(rows), "error", err)
			returnSpanLedgerRows(writeQueue, rows, logger)
			waitSpanLedgerRetry(ctx)
		}
	}
}

// returnSpanLedgerRows runs on its own context, because the store can fail while
// the writer is stopping.
func returnSpanLedgerRows(writeQueue *queue.RedisQueue, rows []model.SpanLedgerRow, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), spanLedgerFinalReportTimeout)
	defer cancel()
	if err := writeQueue.PublishSpanLedger(ctx, rows); err != nil {
		logger.Error("span ledger: rows lost", "rows", len(rows), "error", err)
	}
}

func waitSpanLedgerRetry(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(spanLedgerRetryWait):
	}
}
