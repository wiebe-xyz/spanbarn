package ingest

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

type mockLogsRepo struct {
	mu    sync.Mutex
	calls [][]model.LogRecord
}

func (m *mockLogsRepo) InsertLogs(_ context.Context, recs []model.LogRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, append([]model.LogRecord{}, recs...))
	return nil
}

func (m *mockLogsRepo) totalInserted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		n += len(c)
	}
	return n
}

func makeTestLogBatch(n int) []model.LogRecord {
	recs := make([]model.LogRecord, n)
	for i := range recs {
		recs[i] = model.LogRecord{ProjectID: 1, Body: "test", SeverityNumber: 9}
	}
	return recs
}

func TestLogsHandlerEnqueue(t *testing.T) {
	repo := &mockLogsRepo{}
	h := NewLogsHandler(repo, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.Run(ctx) }()

	h.Enqueue(makeTestLogBatch(5))
	h.Enqueue(makeTestLogBatch(3))

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

func TestLogsHandlerGracefulDrain(t *testing.T) {
	repo := &mockLogsRepo{}
	h := NewLogsHandler(repo, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.Run(ctx) }()

	h.Enqueue(makeTestLogBatch(10))
	cancel()
	wg.Wait()

	if got := repo.totalInserted(); got != 10 {
		t.Errorf("graceful drain: want 10 records, got %d", got)
	}
}

func TestLogsHandlerBackpressure(t *testing.T) {
	repo := &mockLogsRepo{}
	h := NewLogsHandler(repo, slog.Default())

	dropped := false
	for i := 0; i < logsChannelSize+10; i++ {
		before := len(h.ch)
		h.Enqueue(makeTestLogBatch(1))
		if i >= logsChannelSize && before == logsChannelSize {
			dropped = true
		}
	}
	if !dropped {
		t.Error("expected at least one batch to be dropped when channel is full")
	}
}

func TestLogsHandlerFlushOnSizeThreshold(t *testing.T) {
	repo := &mockLogsRepo{}
	h := NewLogsHandler(repo, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.Run(ctx) }()

	h.Enqueue(makeTestLogBatch(logsFlushSize + 1))

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if repo.totalInserted() >= logsFlushSize+1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if got := repo.totalInserted(); got != logsFlushSize+1 {
		t.Errorf("want %d records, got %d", logsFlushSize+1, got)
	}
}
