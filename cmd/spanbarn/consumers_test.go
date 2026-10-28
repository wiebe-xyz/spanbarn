package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// shutdownSource hands out one batch and cancels the consumer's context as it
// does, the way a writer shutdown lands while a batch is on its way.
type shutdownSource struct {
	cancel context.CancelFunc
	served bool
}

func (s *shutdownSource) next(ctx context.Context) (bool, error) {
	if s.served {
		return false, ctx.Err()
	}
	s.served = true
	s.cancel()
	return true, nil
}

func (s *shutdownSource) ConsumeLogs(ctx context.Context) ([]model.LogRecord, error) {
	if ok, err := s.next(ctx); !ok {
		return nil, err
	}
	return []model.LogRecord{{ProjectID: 1, Body: "in flight"}}, nil
}

func (s *shutdownSource) ConsumeMetrics(ctx context.Context) ([]model.MetricRecord, error) {
	if ok, err := s.next(ctx); !ok {
		return nil, err
	}
	return []model.MetricRecord{{ProjectID: 1, Name: "in.flight", Type: model.MetricTypeGauge, TimeUnixNano: uint64(time.Now().UnixNano())}}, nil
}

// recordingSink keeps how many records each insert got and whether its
// context was already cancelled.
type recordingSink struct {
	stored   int
	canceled bool
}

func (r *recordingSink) record(ctx context.Context, n int) error {
	if err := ctx.Err(); err != nil {
		r.canceled = true
		return err
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

// A batch consumed before shutdown is off the queue, so the consumer must
// still store it after the context is cancelled. On testing on 2026-10-28 the
// insert ran on the cancelled context and logged "logs insert error: context
// canceled", and the batch was gone.
func TestConsumersStoreTheBatchInFlightAtShutdown(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	runs := map[string]func(ctx context.Context, src *shutdownSource, sink *recordingSink){
		"logs": func(ctx context.Context, src *shutdownSource, sink *recordingSink) {
			runLogsConsumer(ctx, src, sink, logger)
		},
		"metrics": func(ctx context.Context, src *shutdownSource, sink *recordingSink) {
			acc := aggregation.NewMetricAccumulator(nil, time.Minute, time.Hour, logger)
			runMetricsConsumer(ctx, src, acc, sink, logger)
		},
	}
	for name, run := range runs {
		ctx, cancel := context.WithCancel(context.Background())
		src, sink := &shutdownSource{cancel: cancel}, &recordingSink{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			run(ctx, src, sink)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: consumer did not return after shutdown", name)
		}
		if sink.canceled || sink.stored != 1 {
			t.Errorf("%s: stored %d records, insert saw a cancelled context: %v; want the 1 record stored", name, sink.stored, sink.canceled)
		}
	}
}
