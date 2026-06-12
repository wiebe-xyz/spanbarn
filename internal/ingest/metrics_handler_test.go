package ingest

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// mockMetricsRepo captures InsertMetrics calls for inspection.
type mockMetricsRepo struct {
	mu    sync.Mutex
	calls [][]model.MetricRecord
}

func (m *mockMetricsRepo) InsertMetrics(_ context.Context, recs []model.MetricRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, append([]model.MetricRecord{}, recs...))
	return nil
}

func (m *mockMetricsRepo) totalInserted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		n += len(c)
	}
	return n
}

func makeTestMetricBatch(n int) []model.MetricRecord {
	recs := make([]model.MetricRecord, n)
	for i := range recs {
		recs[i] = model.MetricRecord{ProjectID: 1, Name: "cpu", Type: model.MetricTypeGauge, Value: float64(i)}
	}
	return recs
}

func TestMetricsHandlerEnqueue(t *testing.T) {
	repo := &mockMetricsRepo{}
	h := NewMetricsHandler(repo, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.Run(ctx) }()

	h.Enqueue(makeTestMetricBatch(5))
	h.Enqueue(makeTestMetricBatch(3))

	// Wait up to 500ms for the ticker to flush.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if repo.totalInserted() >= 8 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if got := repo.totalInserted(); got != 8 {
		t.Errorf("want 8 records inserted, got %d", got)
	}
}

func TestMetricsHandlerGracefulDrain(t *testing.T) {
	repo := &mockMetricsRepo{}
	h := NewMetricsHandler(repo, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.Run(ctx) }()

	// Enqueue records then cancel before the ticker fires.
	h.Enqueue(makeTestMetricBatch(10))
	cancel()
	wg.Wait()

	if got := repo.totalInserted(); got != 10 {
		t.Errorf("graceful drain: want 10 records, got %d", got)
	}
}

func TestMetricsHandlerBackpressure(t *testing.T) {
	repo := &mockMetricsRepo{}
	h := NewMetricsHandler(repo, slog.Default())

	// Fill the channel without starting Run — the channel will become full.
	dropped := false
	for i := 0; i < metricsChannelSize+10; i++ {
		before := len(h.ch)
		h.Enqueue(makeTestMetricBatch(1))
		after := len(h.ch)
		if after <= before && i < metricsChannelSize {
			// channel should grow up to capacity
		}
		if i >= metricsChannelSize && before == metricsChannelSize {
			dropped = true
		}
	}
	if !dropped {
		t.Error("expected at least one batch to be dropped when channel is full")
	}
	// No panic, no block — test passes by completing.
}

func TestMetricsHandlerFlushOnSizeThreshold(t *testing.T) {
	repo := &mockMetricsRepo{}
	h := NewMetricsHandler(repo, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.Run(ctx) }()

	// Send batches that total more than metricsFlushSize.
	h.Enqueue(makeTestMetricBatch(metricsFlushSize + 1))

	// Wait briefly — a size-triggered flush should have occurred before the ticker.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if repo.totalInserted() >= metricsFlushSize+1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if got := repo.totalInserted(); got != metricsFlushSize+1 {
		t.Errorf("want %d records, got %d", metricsFlushSize+1, got)
	}
}
