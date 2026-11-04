package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
)

// scriptedSource hands out its batches and cancels the consumer's context with
// the last one, the way a writer shutdown lands while a batch is on its way.
type scriptedSource[T any] struct {
	cancel    context.CancelFunc
	batches   []*queue.Batch[T]
	acks      int
	ackCancel bool
	reclaims  int
	reclaimed queue.Reclaimed
}

func (s *scriptedSource[T]) Consume(ctx context.Context) (*queue.Batch[T], error) {
	if len(s.batches) == 0 {
		s.cancel()
		return nil, ctx.Err()
	}
	batch := s.batches[0]
	s.batches = s.batches[1:]
	if len(s.batches) == 0 {
		s.cancel()
	}
	return batch, nil
}

func (s *scriptedSource[T]) Ack(ctx context.Context, _ *queue.Batch[T]) error {
	s.acks++
	s.ackCancel = s.ackCancel || ctx.Err() != nil
	return nil
}

func (s *scriptedSource[T]) Reclaim(context.Context) (queue.Reclaimed, error) {
	s.reclaims++
	return s.reclaimed, nil
}

func logsSource(cancel context.CancelFunc) *scriptedSource[model.LogRecord] {
	return &scriptedSource[model.LogRecord]{cancel: cancel, batches: []*queue.Batch[model.LogRecord]{
		{Records: []model.LogRecord{{ProjectID: 1, Body: "in flight"}}},
	}}
}

func metricsSource(cancel context.CancelFunc) *scriptedSource[model.MetricRecord] {
	return &scriptedSource[model.MetricRecord]{cancel: cancel, batches: []*queue.Batch[model.MetricRecord]{
		{Records: []model.MetricRecord{{ProjectID: 1, Name: "in.flight", Type: model.MetricTypeGauge, TimeUnixNano: uint64(time.Now().UnixNano())}}},
	}}
}

// recordingSink keeps how many records each insert got and whether its
// context was already cancelled.
type recordingSink struct {
	stored   int
	canceled bool
	err      error
}

func (r *recordingSink) record(ctx context.Context, n int) error {
	if err := ctx.Err(); err != nil {
		r.canceled = true
		return err
	}
	if r.err != nil {
		return r.err
	}
	r.stored += n
	return nil
}

func (r *recordingSink) InsertLogs(ctx context.Context, recs []model.LogRecord) error {
	return r.record(ctx, len(recs))
}

func (r *recordingSink) InsertMetrics(ctx context.Context, recs []model.MetricRecord) error {
	return r.record(ctx, len(recs))
}

// consumerRun runs one consumer over one batch and reports how many acks it got
// and whether an ack saw a cancelled context.
type consumerRun func(ctx context.Context, cancel context.CancelFunc, sink *recordingSink) (acks int, ackCancelled bool)

func consumerRuns(logger *slog.Logger) map[string]consumerRun {
	return map[string]consumerRun{
		"logs": func(ctx context.Context, cancel context.CancelFunc, sink *recordingSink) (int, bool) {
			src := logsSource(cancel)
			runLogsConsumer(ctx, src, sink, logger)
			return src.acks, src.ackCancel
		},
		"metrics": func(ctx context.Context, cancel context.CancelFunc, sink *recordingSink) (int, bool) {
			src := metricsSource(cancel)
			acc := aggregation.NewMetricAccumulator(nil, time.Minute, time.Hour, logger)
			runMetricsConsumer(ctx, src, acc, sink, logger)
			return src.acks, src.ackCancel
		},
	}
}

// A batch consumed before shutdown must still be stored after the context is
// cancelled. On testing on 2026-10-28 the insert ran on the cancelled context and
// logged "logs insert error: context canceled", and the batch was gone. The ack
// must get through as well, or the next writer stores the batch again.
func TestConsumersStoreTheBatchInFlightAtShutdown(t *testing.T) {
	for name, run := range consumerRuns(slog.New(slog.DiscardHandler)) {
		ctx, cancel := context.WithCancel(context.Background())
		sink := &recordingSink{}
		var acks int
		var ackCancelled bool
		done := make(chan struct{})
		go func() {
			defer close(done)
			acks, ackCancelled = run(ctx, cancel, sink)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: consumer did not return after shutdown", name)
		}
		if sink.canceled || sink.stored != 1 {
			t.Errorf("%s: stored %d records, insert saw a cancelled context: %v; want the 1 record stored", name, sink.stored, sink.canceled)
		}
		if acks != 1 || ackCancelled {
			t.Errorf("%s: %d acks, ack saw a cancelled context: %v; want 1 ack on a live context", name, acks, ackCancelled)
		}
	}
}

// A failed insert finishes the batch. On metrics a replay would add it to the
// accumulator again; on logs the error log is the record, as it was before the
// processing list.
func TestFailedInsertFinishesTheBatch(t *testing.T) {
	for name, run := range consumerRuns(slog.New(slog.DiscardHandler)) {
		ctx, cancel := context.WithCancel(context.Background())
		acks, _ := run(ctx, cancel, &recordingSink{err: errors.New("disk stall")})
		cancel()
		if acks != 1 {
			t.Errorf("%s: %d acks after a failed insert, want 1", name, acks)
		}
	}
}

type ledgerSink struct {
	err    error
	stored int
}

func (l *ledgerSink) AddSpanLedgerRows(rows []model.SpanLedgerRow) error {
	if l.err != nil {
		return l.err
	}
	l.stored += len(rows)
	return nil
}

// A missing exit count reads as loss, so a batch the ledger could not store stays
// on the processing list for the reclaim. The store is one transaction, so its
// replay counts nothing twice.
func TestSpanLedgerConsumerAcksOnlyAStoredBatch(t *testing.T) {
	for name, tc := range map[string]struct {
		err      error
		wantAcks int
	}{
		"stored":       {nil, 1},
		"store failed": {errors.New("disk stall"), 0},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		src := &scriptedSource[model.SpanLedgerRow]{cancel: cancel, batches: []*queue.Batch[model.SpanLedgerRow]{
			{Records: []model.SpanLedgerRow{{ProjectID: 1, Accepted: 1}}},
		}}
		runSpanLedgerConsumer(ctx, src, &ledgerSink{err: tc.err}, slog.New(slog.DiscardHandler))
		cancel()
		if src.acks != tc.wantAcks {
			t.Errorf("%s: %d acks, want %d", name, src.acks, tc.wantAcks)
		}
	}
}

// A batch that panics the store would panic every replay.
func TestPanicInStoreFinishesTheBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := logsSource(cancel)
	c := newBatchConsumer[model.LogRecord]("logs", src, func(context.Context, []model.LogRecord) bool {
		panic("batch cannot be stored")
	}, slog.New(slog.DiscardHandler))
	func() {
		defer func() { _ = recover() }()
		c.run(ctx)
	}()
	if src.acks != 1 {
		t.Errorf("%d acks after a panic, want 1", src.acks)
	}
}

// A deleted or evicted writer pod is replaced at once while the old one drains
// its last batch. The new writer must leave that batch alone during the delay, or
// both writers store it.
func TestConsumerReclaimsOnlyAfterTheDelay(t *testing.T) {
	started := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		elapsed      time.Duration
		wantReclaims bool
	}{
		"during the delay": {queue.ReclaimDelay - time.Second, false},
		"after the delay":  {queue.ReclaimDelay, true},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		src := logsSource(cancel)
		src.reclaimed = queue.Reclaimed{Batches: 1, Records: 1}
		var logs bytes.Buffer
		c := newBatchConsumer[model.LogRecord]("logs", src, func(context.Context, []model.LogRecord) bool { return true }, slog.New(slog.NewTextHandler(&logs, nil)))
		calls := 0
		c.now = func() time.Time {
			calls++
			if calls == 1 {
				return started
			}
			return started.Add(tc.elapsed)
		}
		c.run(ctx)
		cancel()
		if got := src.reclaims > 0; got != tc.wantReclaims {
			t.Errorf("%s: %d reclaims, want any: %v", name, src.reclaims, tc.wantReclaims)
		}
		if got := strings.Contains(logs.String(), "reclaimed batches"); got != tc.wantReclaims {
			t.Errorf("%s: reclaim warning logged: %v, want %v", name, got, tc.wantReclaims)
		}
	}
}

// The whole path on a real list: a batch a lost reply left on the processing list
// is stored once after the delay and leaves no list behind.
func TestConsumerStoresABatchLeftOnTheProcessingList(t *testing.T) {
	mr := miniredis.RunT(t)
	q, err := queue.NewRedisQueueWithRetry(context.Background(), "redis://"+mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	raw, err := json.Marshal([]model.LogRecord{{ProjectID: 1, Body: "left behind"}})
	if err != nil {
		t.Fatal(err)
	}
	processing := queue.LogsQueueKey + ":processing"
	mr.Lpush(processing, string(raw))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stored int
	c := newBatchConsumer[model.LogRecord]("logs", q.Logs(), func(_ context.Context, recs []model.LogRecord) bool {
		stored += len(recs)
		cancel()
		return true
	}, slog.New(slog.DiscardHandler))
	started := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	calls := 0
	c.now = func() time.Time {
		calls++
		if calls == 1 {
			return started
		}
		return started.Add(queue.ReclaimDelay)
	}
	c.run(ctx)

	if stored != 1 {
		t.Errorf("stored %d records, want 1", stored)
	}
	if mr.Exists(processing) || mr.Exists(queue.LogsQueueKey) {
		t.Error("the batch is still on a list after it was stored")
	}
}
