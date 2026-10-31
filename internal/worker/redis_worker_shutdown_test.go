package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanledger"
)

// ctxRepo fails a write whose context is done, as the real repository does.
type ctxRepo struct{ mockRepo }

func (r *ctxRepo) InsertSpansStaging(ctx context.Context, spans []repository.Span) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.mockRepo.InsertSpansStaging(ctx, spans)
}

func (r *ctxRepo) InsertSpans(ctx context.Context, spans []repository.Span) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.mockRepo.InsertSpans(ctx, spans)
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

var shutdownBatch = []model.SpanRecord{
	{ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "op", Service: "svc", Status: "error", DurationUs: 10},
}

// A batch popped off the queue has nowhere to go back to, so the shutdown that
// cancels the worker still has to store it. Rollouts that caught a write in
// flight lost one with no log and no ledger count (#478).
func TestRedisWorkerStoresTheBatchInFlightAtShutdown(t *testing.T) {
	for _, staging := range []bool{true, false} {
		tally := spanledger.New()
		repo := &ctxRepo{}
		rw := &RedisWorker{repo: repo, logger: slog.Default()}
		rw.SetStagingMode(staging)
		rw.SetAccumulator(&mockAccumulator{})
		rw.SetConfig(WorkerConfig{SlowThresholdUs: 1_000_000})
		rw.SetLedger(tally)

		rw.processBatch(cancelledContext(), shutdownBatch)

		if got := len(repo.getSpans()); got != 1 {
			t.Errorf("staging=%v: stored %d spans, want 1", staging, got)
		}
		if processed, failed := ledgerTotals(t, tally); processed != 1 || failed != 0 {
			t.Errorf("staging=%v: processed = %d, failed = %d, want 1 and 0", staging, processed, failed)
		}
	}
}

// A write that fails while the worker shuts down stops retrying and counts as
// lost, in the log and in the ledger.
func TestRedisWorkerCountsAStagedBatchItCouldNotStoreAtShutdown(t *testing.T) {
	var logs bytes.Buffer
	tally := spanledger.New()
	rw := &RedisWorker{
		repo:   &mockRepo{err: errors.New("database is locked (5)")},
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}
	rw.SetStagingMode(true)
	rw.SetLedger(tally)

	rw.processBatch(cancelledContext(), shutdownBatch)

	if processed, failed := ledgerTotals(t, tally); processed != 0 || failed != 1 {
		t.Errorf("processed = %d, failed = %d, want 0 and 1", processed, failed)
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("loss not logged at ERROR:\n%s", logs.String())
	}
}
