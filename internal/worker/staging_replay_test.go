package worker

import (
	"context"
	"fmt"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// A batch replayed from the processing list stages its spans twice. The flush
// stores and counts one copy and clears both rows.
func TestStagingFlushStoresAReplayedSpanOnce(t *testing.T) {
	repo := &mockStagingRepo{
		ready: []string{"t1"},
		spans: []repository.Span{
			{ID: 1, ProjectID: 1, TraceID: "t1", SpanID: "root", Status: "error", DurationUs: 1},
			{ID: 2, ProjectID: 1, TraceID: "t1", SpanID: "child", Status: "unset", DurationUs: 1},
			{ID: 3, ProjectID: 1, TraceID: "t1", SpanID: "root", Status: "error", DurationUs: 1},
			{ID: 4, ProjectID: 1, TraceID: "t1", SpanID: "child", Status: "unset", DurationUs: 1},
		},
	}
	acc := &countingAcc{}
	f := NewStagingFlusher(repo, repo, StagingFlusherConfig{SlowThresholdUs: 1000}, nil)
	f.SetAccumulator(acc)

	if _, err := f.flushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if acc.n != 2 {
		t.Errorf("accumulator counted %d spans, want 2", acc.n)
	}
	if len(repo.committed) != 1 || len(repo.committed[0]) != 2 {
		t.Fatalf("committed %v, want the 2 spans once", repo.committed)
	}
	// The whole-trace commit deletes by trace id, which takes the copies too.
	if len(repo.committedTraces[0]) != 1 || repo.committedTraces[0][0] != "t1" {
		t.Errorf("committed traces %v, want t1", repo.committedTraces[0])
	}
}

func TestDropReplayedSpansKeepsDifferentSpans(t *testing.T) {
	spans := []repository.Span{
		{ID: 1, ProjectID: 1, TraceID: "t1", SpanID: "s1"},
		{ID: 2, ProjectID: 2, TraceID: "t1", SpanID: "s1"}, // another project
		{ID: 3, ProjectID: 1, TraceID: "t2", SpanID: "s1"}, // another trace
		{ID: 4, ProjectID: 1, TraceID: "t1", SpanID: ""},   // nothing to compare
		{ID: 5, ProjectID: 1, TraceID: "t1", SpanID: ""},
	}
	unique, replayed := dropReplayedSpans(spans)
	if len(unique) != len(spans) || len(replayed) != 0 {
		t.Errorf("kept %d of %d spans, replayed %v", len(unique), len(spans), replayed)
	}
}

// A part deletes only the rows it names, so a copy it did not name would stay
// staged and count as a new span on the next flush.
func TestStagingFlushClearsReplayedRowsOfAnOverCapTrace(t *testing.T) {
	var spans []repository.Span
	for i := 0; i < stagingChunkRows+10; i++ {
		spans = append(spans, repository.Span{
			ID: int64(i + 1), ProjectID: 1, TraceID: "big", SpanID: fmt.Sprintf("s%d", i),
			Status: "unset", DurationUs: 1,
		})
	}
	unique := len(spans)
	// Copies of the first span, which lands in the first part, and of the last.
	copies := []repository.Span{spans[0], spans[unique-1]}
	copies[0].ID, copies[1].ID = int64(unique+1), int64(unique+2)
	spans = append(spans, copies...)

	repo := &mockStagingRepo{ready: []string{"big"}, spans: spans}
	acc := &countingAcc{}
	f := NewStagingFlusher(repo, repo, StagingFlusherConfig{SlowThresholdUs: 1_000_000}, nil)
	f.SetAccumulator(acc)

	if _, err := f.flushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if acc.n != unique {
		t.Errorf("accumulator counted %d spans, want %d", acc.n, unique)
	}
	if len(repo.parts) < 2 {
		t.Fatalf("flush made %d parts, want the trace cut in parts", len(repo.parts))
	}
	cleared := 0
	for _, p := range repo.parts {
		cleared += p.staged
	}
	if cleared != len(spans) {
		t.Errorf("parts cleared %d staged rows, want all %d", cleared, len(spans))
	}
	if repo.parts[0].staged != stagingChunkRows+1 {
		t.Errorf("first part cleared %d rows, want its %d plus the copy of its first span", repo.parts[0].staged, stagingChunkRows)
	}
}
