package worker

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type mockStagingRepo struct {
	ready           []string
	spans           []repository.Span
	committed       [][]repository.Span
	committedTraces [][]string
	gcCutoff        time.Time
	gcDeleted       int64
	count           int64
}

func (m *mockStagingRepo) ReadyStagingTraceIDs(_ context.Context, _ time.Time, _ int) ([]string, error) {
	r := m.ready
	m.ready = nil // one-shot so the drain loop terminates
	return r, nil
}
func (m *mockStagingRepo) GetStagingSpansByTraceIDs(_ context.Context, _ []string) ([]repository.Span, error) {
	return m.spans, nil
}
func (m *mockStagingRepo) CommitStagingFlush(_ context.Context, traceIDs []string, interesting []repository.Span) error {
	m.committedTraces = append(m.committedTraces, traceIDs)
	m.committed = append(m.committed, interesting)
	return nil
}
func (m *mockStagingRepo) DeleteStagingOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	m.gcCutoff = cutoff
	return m.gcDeleted, nil
}
func (m *mockStagingRepo) CountStagingRows(_ context.Context) (int64, error)     { return m.count, nil }
func (m *mockStagingRepo) InsertPromptRecords(_ []repository.PromptRecord) error { return nil }

type countingAcc struct{ n int }

func (c *countingAcc) Add(_ repository.Span) { c.n++ }

func TestStagingFlusherFlushOnce(t *testing.T) {
	repo := &mockStagingRepo{
		ready: []string{"tA", "tB"},
		spans: []repository.Span{
			{TraceID: "tA", SpanID: "a1", Status: "unset", DurationUs: 1},
			{TraceID: "tA", SpanID: "a2", Status: "error", DurationUs: 1},
			{TraceID: "tB", SpanID: "b1", Status: "unset", DurationUs: 1},
		},
	}
	acc := &countingAcc{}
	f := NewStagingFlusher(repo, repo, StagingFlusherConfig{SlowThresholdUs: 1000}, nil)
	f.SetAccumulator(acc)

	n, err := f.flushOnce(context.Background())
	if err != nil {
		t.Fatalf("flushOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 traces flushed, got %d", n)
	}
	if acc.n != 3 {
		t.Fatalf("accumulator must see all 3 spans (boring included), got %d", acc.n)
	}
	if len(repo.committed) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(repo.committed))
	}
	// Only trace A (has the error span) is interesting -> its 2 spans stored;
	// boring trace B is dropped.
	if got := len(repo.committed[0]); got != 2 {
		t.Fatalf("expected 2 interesting spans stored (trace A), got %d", got)
	}
	// Both traces are deleted from staging regardless of interesting-ness.
	if len(repo.committedTraces[0]) != 2 {
		t.Fatalf("expected both traces deleted from staging, got %v", repo.committedTraces[0])
	}
}

func TestStagingFlusherGCUsesMaxAge(t *testing.T) {
	repo := &mockStagingRepo{gcDeleted: 3, count: 10}
	f := NewStagingFlusher(repo, repo, StagingFlusherConfig{MaxAge: 10 * time.Minute}, nil)
	f.gcOnce(context.Background())
	if age := time.Since(repo.gcCutoff); age < 9*time.Minute || age > 11*time.Minute {
		t.Fatalf("gc cutoff should be ~MaxAge ago, was %v ago", age)
	}
}
