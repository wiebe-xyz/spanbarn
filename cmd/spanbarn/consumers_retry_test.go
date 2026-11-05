package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// flakySink fails its first fails inserts with err, then stores. onFail runs
// after each failure.
type flakySink struct {
	fails    int
	err      error
	attempts int
	stored   int
	onFail   func()
}

func (f *flakySink) insert(n int) error {
	f.attempts++
	if f.fails < 0 || f.attempts <= f.fails {
		if f.onFail != nil {
			f.onFail()
		}
		return f.err
	}
	f.stored += n
	return nil
}

func (f *flakySink) InsertLogs(_ context.Context, recs []model.LogRecord) error {
	return f.insert(len(recs))
}

func (f *flakySink) InsertMetrics(_ context.Context, recs []model.MetricRecord) error {
	return f.insert(len(recs))
}

// fastStoreRetries shortens the waits between store attempts.
func fastStoreRetries(t *testing.T) {
	t.Helper()
	backoff, diskFull := storeRetryBackoff, storeDiskFullWait
	storeRetryBackoff, storeDiskFullWait = time.Millisecond, time.Millisecond
	t.Cleanup(func() { storeRetryBackoff, storeDiskFullWait = backoff, diskFull })
}

// runLogsOnce runs the logs consumer over one batch on a live context; the
// source cancels it only on the next consume.
func runLogsOnce(t *testing.T, sink *flakySink, cancelOnFail bool) (acks int, logs string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &scriptedSource[model.LogRecord]{cancel: cancel, batches: []*queue.Batch[model.LogRecord]{
		{Records: []model.LogRecord{{ProjectID: 1, Body: "line"}}},
		nil,
	}}
	if cancelOnFail {
		sink.onFail = cancel
	}
	var buf bytes.Buffer
	runLogsConsumer(ctx, src, sink, slog.New(slog.NewTextHandler(&buf, nil)))
	return src.acks, buf.String()
}

// A failed logs insert lost its lines: the batch was acked after one attempt
// (#496). The insert is one transaction, so trying again stores nothing twice.
func TestLogsConsumerRetriesAFailedInsert(t *testing.T) {
	fastStoreRetries(t)
	sink := &flakySink{fails: 2, err: errors.New("disk stall")}
	acks, _ := runLogsOnce(t, sink, false)
	if sink.stored != 1 || sink.attempts != 3 {
		t.Errorf("stored %d records in %d attempts, want 1 in 3", sink.stored, sink.attempts)
	}
	if acks != 1 {
		t.Errorf("%d acks, want 1", acks)
	}
}

// A batch that never stores stops after storeAttempts and is finished with one
// error, so it does not hold up the queue behind it.
func TestLogsConsumerDropsABatchThatNeverStores(t *testing.T) {
	fastStoreRetries(t)
	sink := &flakySink{fails: -1, err: errors.New("constraint failed")}
	acks, logs := runLogsOnce(t, sink, false)
	if sink.attempts != storeAttempts {
		t.Errorf("%d attempts, want %d", sink.attempts, storeAttempts)
	}
	if acks != 1 {
		t.Errorf("%d acks, want 1", acks)
	}
	var errs []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level=ERROR") {
			errs = append(errs, line)
		}
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "logs insert failed after retries, batch dropped") || !strings.Contains(errs[0], "records=1") {
		t.Errorf("want one error naming the dropped batch, got %q", errs)
	}
}

// A shutdown during the retries leaves the batch on the processing list, so the
// next writer stores it.
func TestLogsConsumerLeavesTheBatchAtShutdown(t *testing.T) {
	fastStoreRetries(t)
	sink := &flakySink{fails: -1, err: errors.New("disk stall")}
	acks, _ := runLogsOnce(t, sink, true)
	if sink.attempts != 1 {
		t.Errorf("%d attempts, want 1: the shutdown ends the retries", sink.attempts)
	}
	if acks != 0 {
		t.Errorf("%d acks, want 0: the batch is left for the next writer", acks)
	}
}

// A full disk is not counted against the attempts. Retention frees space within
// a cycle, so the batch waits for it.
func TestLogsConsumerWaitsOutAFullDisk(t *testing.T) {
	fastStoreRetries(t)
	sink := &flakySink{fails: storeAttempts + 2, err: errors.New("database or disk is full")}
	acks, logs := runLogsOnce(t, sink, false)
	if sink.stored != 1 {
		t.Errorf("stored %d records after %d attempts, want 1", sink.stored, sink.attempts)
	}
	if acks != 1 {
		t.Errorf("%d acks, want 1", acks)
	}
	if n := strings.Count(logs, "level=ERROR"); n != 1 {
		t.Errorf("%d error lines for one full disk, want 1:\n%s", n, logs)
	}
}

type rollupSink struct{ rollups []repository.MetricRollup }

func (r *rollupSink) UpsertMetricRollups(rollups []repository.MetricRollup) error {
	r.rollups = append(r.rollups, rollups...)
	return nil
}

// A failed raw metrics insert lost its rows (#496). The accumulator has already
// counted the points, so only the raw insert is tried again, and below the
// repeat filter: the filter has recorded the point as written and would hold it
// back as a repeat on a second pass.
func TestMetricsConsumerRetriesTheRawInsertBelowTheFilter(t *testing.T) {
	fastStoreRetries(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	point := model.MetricRecord{
		ProjectID: 1, Name: "requests", Type: model.MetricTypeSum, Temporality: "cumulative",
		StartTimeUnixNano: uint64(at.Add(-time.Hour).UnixNano()), TimeUnixNano: uint64(at.UnixNano()), Value: 7,
	}
	src := &scriptedSource[model.MetricRecord]{cancel: cancel, batches: []*queue.Batch[model.MetricRecord]{
		{Records: []model.MetricRecord{point}}, nil,
	}}
	raw := &flakySink{fails: 1, err: errors.New("disk stall")}
	rollups := &rollupSink{}
	acc := aggregation.NewMetricAccumulator(rollups, time.Minute, time.Hour, slog.New(slog.DiscardHandler))
	writer := aggregation.NewFilteredMetricWriter(raw, aggregation.NewMetricRepeatFilter(time.Hour))

	runMetricsConsumer(ctx, src, acc, writer, slog.New(slog.DiscardHandler))

	if raw.stored != 1 || raw.attempts != 2 {
		t.Errorf("raw insert stored %d points in %d attempts, want 1 in 2", raw.stored, raw.attempts)
	}
	if src.acks != 1 {
		t.Errorf("%d acks, want 1", src.acks)
	}
	if err := acc.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rollups.rollups) != 1 || rollups.rollups[0].Count != 1 {
		t.Errorf("accumulator rollups %+v, want one bucket counting the point once", rollups.rollups)
	}
}
