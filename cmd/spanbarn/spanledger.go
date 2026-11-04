package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spanledger"
)

const (
	// spanLedgerFinalReportTimeout bounds the last report of a stopping pod,
	// which has to finish inside the termination grace period.
	spanLedgerFinalReportTimeout = 5 * time.Second

	// spanLedgerRetryWait keeps the consumer from spinning on a batch it cannot
	// store.
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

// spanLedgerStore is the ledger's storage side.
type spanLedgerStore interface {
	AddSpanLedgerRows(rows []model.SpanLedgerRow) error
}

// runSpanLedgerConsumer stores the ledger rows reader pods publish. A batch it
// cannot store stays on the processing list for the reclaim, because a missing
// exit count reads as loss. The store is one transaction, so a failed one leaves
// nothing to count twice. A writer killed after the commit does replay the batch:
// the ledger then reads high by those rows, and the reclaim warning names it.
func runSpanLedgerConsumer(ctx context.Context, q batchSource[model.SpanLedgerRow], repo spanLedgerStore, logger *slog.Logger) {
	newBatchConsumer("span ledger", q, func(_ context.Context, rows []model.SpanLedgerRow) bool {
		if err := repo.AddSpanLedgerRows(rows); err != nil {
			logger.Warn("span ledger: store failed, leaving rows for the reclaim", "rows", len(rows), "error", err)
			waitSpanLedgerRetry(ctx)
			return false
		}
		return true
	}, logger).run(ctx)
}

func waitSpanLedgerRetry(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(spanLedgerRetryWait):
	}
}
