package worker

import (
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanledger"
)

// SetLedger makes the worker count the spans it handles in t (#471).
func (w *RedisWorker) SetLedger(t *spanledger.Tally) {
	w.ledger = t
}

// settleLedger counts a handled batch: every span as processed except those in
// lost, which the worker gave up on. A batch that goes back to the queue is not
// handled yet and is counted when it comes round again.
func (w *RedisWorker) settleLedger(spans, lost []repository.Span) {
	for i := range spans {
		w.ledger.Add(spanledger.Processed, spans[i].ProjectID, spans[i].StartTimeUs, 1)
	}
	for i := range lost {
		w.ledger.Add(spanledger.Processed, lost[i].ProjectID, lost[i].StartTimeUs, -1)
		w.ledger.Add(spanledger.Failed, lost[i].ProjectID, lost[i].StartTimeUs, 1)
	}
}
