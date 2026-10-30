package worker

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spanledger"
)

func ledgerTotals(t *testing.T, tally *spanledger.Tally) (processed, failed int64) {
	t.Helper()
	for _, r := range tally.Take() {
		processed += r.Processed
		failed += r.Failed
	}
	return processed, failed
}

// Boring spans that are aggregated and not stored are handled too, so the
// ledger counts them as processed.
func TestRedisWorkerLedgerCountsHandledSpans(t *testing.T) {
	tally := spanledger.New()
	rw := &RedisWorker{repo: &mockRepo{}, logger: slog.Default()}
	rw.SetAccumulator(&mockAccumulator{})
	rw.SetConfig(WorkerConfig{SlowThresholdUs: 1_000_000})
	rw.SetLedger(tally)

	rw.processBatch(context.Background(), []model.SpanRecord{
		{ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "op", Service: "svc", Status: "OK", DurationUs: 10},
		{ProjectID: 1, TraceID: "t2", SpanID: "s2", Name: "op", Service: "svc", Status: "error", DurationUs: 10},
	})
	if processed, failed := ledgerTotals(t, tally); processed != 2 || failed != 0 {
		t.Errorf("processed = %d, failed = %d, want 2 and 0", processed, failed)
	}
}

// A batch that cannot be written counts its stored spans as failed. The boring
// span of the batch was aggregated, so it stays processed.
func TestRedisWorkerLedgerCountsDeadLetteredSpans(t *testing.T) {
	tally := spanledger.New()
	rw := &RedisWorker{repo: &mockRepo{err: errors.New("database is locked (5)")}, logger: slog.Default()}
	rw.SetAccumulator(&mockAccumulator{})
	rw.SetConfig(WorkerConfig{SlowThresholdUs: 1_000_000})
	rw.SetLedger(tally)

	rw.processBatch(context.Background(), []model.SpanRecord{
		{ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "op", Service: "svc", Status: "OK", DurationUs: 10},
		{ProjectID: 1, TraceID: "t2", SpanID: "s2", Name: "op", Service: "svc", Status: "error", DurationUs: 10},
	})
	processed, failed := ledgerTotals(t, tally)
	if failed == 0 || processed+failed != 2 {
		t.Errorf("processed = %d, failed = %d, want the error span failed and 2 in all", processed, failed)
	}
}

func TestRedisWorkerLedgerCountsStagedSpans(t *testing.T) {
	tally := spanledger.New()
	rw := &RedisWorker{repo: &mockRepo{}, logger: slog.Default()}
	rw.SetStagingMode(true)
	rw.SetLedger(tally)

	rw.processBatch(context.Background(), []model.SpanRecord{
		{ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "op", Service: "svc", Status: "OK", DurationUs: 10},
	})
	if processed, failed := ledgerTotals(t, tally); processed != 1 || failed != 0 {
		t.Errorf("processed = %d, failed = %d, want 1 and 0", processed, failed)
	}
}
