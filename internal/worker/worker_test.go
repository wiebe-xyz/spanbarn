package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

// mockRepo implements the Repository interface for testing.
type mockRepo struct {
	mu    sync.Mutex
	spans []repository.Span
	err   error
	calls int
}

func (m *mockRepo) InsertSpans(_ context.Context, spans []repository.Span) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return m.err
	}
	m.spans = append(m.spans, spans...)
	return nil
}

func (m *mockRepo) InsertPromptRecords(_ context.Context, _ []repository.PromptRecord) error {
	return nil
}

func (m *mockRepo) getSpans() []repository.Span {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]repository.Span, len(m.spans))
	copy(cp, m.spans)
	return cp
}

func (m *mockRepo) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestWorkerProcessesBatch(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.NewSpool(dir, spool.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	// Write some records to spool.
	records := make([]model.SpanRecord, 5)
	for i := range records {
		records[i] = model.SpanRecord{
			ProjectID:   1,
			TraceID:     fmt.Sprintf("trace-%d", i),
			SpanID:      fmt.Sprintf("span-%d", i),
			Name:        fmt.Sprintf("op-%d", i),
			Service:     "test-svc",
			Kind:        "SERVER",
			Status:      "OK",
			StartTimeUs: int64(i * 1000),
			DurationUs:  500,
		}
	}
	if err := sp.Write(records); err != nil {
		t.Fatal(err)
	}

	repo := &mockRepo{}
	logger := slog.Default()
	w := NewWorker(sp, repo, logger)

	w.ProcessOnce(context.Background())

	got := repo.getSpans()
	if len(got) != 5 {
		t.Fatalf("ProcessOnce inserted %d spans, want 5", len(got))
	}
	for i, s := range got {
		want := fmt.Sprintf("span-%d", i)
		if s.SpanID != want {
			t.Errorf("spans[%d].SpanID = %q, want %q", i, s.SpanID, want)
		}
	}

	// Verify cursor advanced: another ProcessOnce should find nothing new.
	w.ProcessOnce(context.Background())
	if len(repo.getSpans()) != 5 {
		t.Fatalf("second ProcessOnce should not have added more spans, got %d", len(repo.getSpans()))
	}
}

func TestWorkerRetries(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.NewSpool(dir, spool.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	records := []model.SpanRecord{{
		ProjectID: 1,
		SpanID:    "retry-span",
		Name:      "op",
		Service:   "svc",
		Kind:      "SERVER",
		Status:    "OK",
	}}
	if err := sp.Write(records); err != nil {
		t.Fatal(err)
	}

	repo := &mockRepo{err: fmt.Errorf("storage unavailable")}
	w := NewWorker(sp, repo, slog.Default())

	w.ProcessOnce(context.Background())

	// Should have retried 3 times.
	if repo.getCalls() != 3 {
		t.Fatalf("expected 3 retry calls, got %d", repo.getCalls())
	}

	// Error count should be incremented.
	_, errCount, _ := w.GetMetrics()
	if errCount != 1 {
		t.Fatalf("error count = %d, want 1", errCount)
	}
}

func TestWorkerGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.NewSpool(dir, spool.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	records := make([]model.SpanRecord, 3)
	for i := range records {
		records[i] = model.SpanRecord{
			ProjectID: 1,
			SpanID:    fmt.Sprintf("shutdown-span-%d", i),
			Name:      "op",
			Service:   "svc",
			Kind:      "SERVER",
			Status:    "OK",
		}
	}
	if err := sp.Write(records); err != nil {
		t.Fatal(err)
	}

	repo := &mockRepo{}
	w := NewWorker(sp, repo, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Cancel immediately — the worker should still process the final batch.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Worker exited.
	case <-time.After(5 * time.Second):
		t.Fatal("Worker did not shut down within 5 seconds")
	}

	got := repo.getSpans()
	if len(got) != 3 {
		t.Fatalf("after shutdown, got %d spans, want 3", len(got))
	}
}
