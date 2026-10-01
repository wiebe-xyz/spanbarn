package retention

import "context"

// maxStructureBackfillPerCycle caps how many trace summaries one retention cycle
// backfills (has_root, orphan_count; see migration 035). The backfill is a
// background convenience, so it runs in small write-connection batches and
// leaves the rest to later cycles. At the default cycle this drains a table of
// millions of summaries within days without ever holding the writer for long.
const maxStructureBackfillPerCycle = 20_000

// backfillTraceStructure advances the lazy has_root / orphan_count backfill.
// A failure is logged and skipped: the columns are an index for the trace list,
// and a retention cycle must not abort before it deletes anything because of it.
func (w *RetentionWorker) backfillTraceStructure(ctx context.Context) {
	n, more, err := w.repo.BackfillTraceStructure(ctx, maxStructureBackfillPerCycle)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down; the next process picks the backlog up
		}
		w.logger.Warn("retention: trace structure backfill failed", "error", err, "filled", n)
		return
	}
	if n > 0 {
		w.logger.Info("retention: trace structure backfill", "filled", n, "more_remain", more)
	}
}
