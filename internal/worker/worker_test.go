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
	w.retryBaseDelay = time.Millisecond

	w.ProcessOnce(context.Background())

	// Should have retried maxRetries times.
	if repo.getCalls() != maxRetries {
		t.Fatalf("expected %d retry calls, got %d", maxRetries, repo.getCalls())
	}

	// Error count should be incremented.
	_, errCount, _ := w.GetMetrics()
	if errCount != 1 {
		t.Fatalf("error count = %d, want 1", errCount)
	}
}

// mockAggregator records spans passed to AggregateSpans for test assertions.
type mockAggregator struct {
	mu    sync.Mutex
	spans []repository.Span
	err   error
}

func (m *mockAggregator) AggregateSpans(_ context.Context, spans []repository.Span) ([]repository.Aggregate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	m.spans = append(m.spans, spans...)
	return nil, nil
}

func (m *mockAggregator) Persist(_ context.Context, _ []repository.Aggregate) error { return nil }

func (m *mockAggregator) getSpans() []repository.Span {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]repository.Span, len(m.spans))
	copy(cp, m.spans)
	return cp
}

func TestWorkerInlineAggregation(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.NewSpool(dir, spool.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	records := make([]model.SpanRecord, 3)
	for i := range records {
		records[i] = model.SpanRecord{
			ProjectID: 1, SpanID: fmt.Sprintf("agg-span-%d", i),
			Name: "op", Service: "svc", Kind: "SERVER", Status: "OK",
		}
	}
	if err := sp.Write(records); err != nil {
		t.Fatal(err)
	}

	repo := &mockRepo{}
	agg := &mockAggregator{}
	w := NewWorker(sp, repo, slog.Default())
	w.SetAggregator(agg)
	w.ProcessOnce(context.Background())

	if len(repo.getSpans()) != 3 {
		t.Fatalf("expected 3 spans inserted, got %d", len(repo.getSpans()))
	}
	if len(agg.getSpans()) != 3 {
		t.Fatalf("expected 3 spans aggregated inline, got %d", len(agg.getSpans()))
	}
}

func TestWorkerNoAggregationOnInsertFailure(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.NewSpool(dir, spool.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	_ = sp.Write([]model.SpanRecord{{ProjectID: 1, SpanID: "s", Name: "op", Service: "svc"}})

	repo := &mockRepo{err: fmt.Errorf("db down")}
	agg := &mockAggregator{}
	w := NewWorker(sp, repo, slog.Default())
	w.SetAggregator(agg)
	w.retryBaseDelay = time.Millisecond
	w.ProcessOnce(context.Background())

	if len(agg.getSpans()) != 0 {
		t.Fatal("aggregator should not be called when insert fails")
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

func TestClassifyInteresting(t *testing.T) {
	spans := []repository.Span{
		{TraceID: "t1", SpanID: "s1", Status: "ok", DurationUs: 100},       // boring
		{TraceID: "t2", SpanID: "s2", Status: "error", DurationUs: 100},    // error → interesting
		{TraceID: "t2", SpanID: "s3", Status: "ok", DurationUs: 100},       // boring but co-trace with error → promoted
		{TraceID: "t3", SpanID: "s4", Status: "ok", DurationUs: 2_000_000}, // slow → interesting
		{TraceID: "t3", SpanID: "s5", Status: "ok", DurationUs: 50},        // boring but co-trace with slow → promoted
	}

	result := classifyInteresting(spans, 1_000_000)

	if len(result) != 4 {
		t.Fatalf("expected 4 interesting spans, got %d", len(result))
	}
	// t1 (boring, no interesting co-trace) must be absent.
	for _, s := range result {
		if s.SpanID == "s1" {
			t.Error("boring span s1 should not appear in interesting set")
		}
	}
}

func TestClassifyInterestingAllBoring(t *testing.T) {
	spans := []repository.Span{
		{TraceID: "t1", SpanID: "s1", Status: "ok", DurationUs: 50},
		{TraceID: "t2", SpanID: "s2", Status: "ok", DurationUs: 100},
	}
	result := classifyInteresting(spans, 1_000_000)
	if result != nil {
		t.Fatalf("expected nil for all-boring batch, got %v", result)
	}
}

func TestClassifyInterestingNoThreshold(t *testing.T) {
	spans := []repository.Span{
		{TraceID: "t1", SpanID: "s1", Status: "ok", DurationUs: 50},
	}
	// threshold=0 means no filter — all spans are interesting.
	result := classifyInteresting(spans, 0)
	if len(result) != 1 {
		t.Fatalf("threshold=0 should pass all spans, got %d", len(result))
	}
}

// mockAccumulator records Add calls for testing.
type mockAccumulator struct {
	mu    sync.Mutex
	added []repository.Span
}

func (m *mockAccumulator) Add(s repository.Span) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.added = append(m.added, s)
}

func (m *mockAccumulator) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.added)
}

func TestRedisWorkerBoringBypass(t *testing.T) {
	repo := &mockRepo{}
	acc := &mockAccumulator{}

	records := []model.SpanRecord{
		{ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "op", Service: "svc", Status: "OK", DurationUs: 50},        // boring
		{ProjectID: 1, TraceID: "t2", SpanID: "s2", Name: "op", Service: "svc", Status: "error", DurationUs: 50},     // error
		{ProjectID: 1, TraceID: "t2", SpanID: "s3", Name: "op", Service: "svc", Status: "OK", DurationUs: 50},        // boring, co-trace with error → promoted
		{ProjectID: 1, TraceID: "t3", SpanID: "s4", Name: "op", Service: "svc", Status: "OK", DurationUs: 2_000_000}, // slow
	}

	rw := &RedisWorker{repo: repo, logger: slog.Default()}
	rw.SetAccumulator(acc)
	rw.SetConfig(WorkerConfig{SlowThresholdUs: 1_000_000})

	rw.processBatch(context.Background(), records)

	// All 4 spans fed to accumulator.
	if acc.count() != 4 {
		t.Errorf("accumulator received %d spans, want 4", acc.count())
	}

	// Only 3 interesting spans written to SQLite (s1 is boring with no interesting co-trace).
	inserted := repo.getSpans()
	if len(inserted) != 3 {
		t.Errorf("inserted %d spans to SQLite, want 3", len(inserted))
	}
	for _, s := range inserted {
		if s.SpanID == "s1" {
			t.Error("boring span s1 should not be in SQLite")
		}
	}
}
