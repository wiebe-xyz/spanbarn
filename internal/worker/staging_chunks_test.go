package worker

import (
	"context"
	"fmt"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func traceSpans(traceID, status string, n int) []repository.Span {
	out := make([]repository.Span, n)
	for i := range out {
		out[i] = repository.Span{TraceID: traceID, SpanID: fmt.Sprintf("%s-%d", traceID, i), Status: status, DurationUs: 1}
	}
	return out
}

func chunkTraces(chunks []stagingChunk) string {
	out := ""
	for _, c := range chunks {
		out += fmt.Sprint(c.traceIDs, c.rows)
	}
	return out
}

// Chunks hold whole traces in batch order up to the row cap, a trace over the
// cap gets a chunk of its own, and each chunk carries only its own spans.
func TestStagingChunksSplitsWholeTraces(t *testing.T) {
	a, b, c, d := traceSpans("a", "unset", 4), traceSpans("b", "unset", 3), traceSpans("c", "unset", 12), traceSpans("d", "unset", 1)
	spans := append(append(append(append([]repository.Span{}, a...), b...), c...), d...)
	interesting := append(append([]repository.Span{}, a[:2]...), d...)

	chunks := stagingChunks([]string{"a", "b", "c", "d"}, spans, interesting, 10)
	if got := chunkTraces(chunks); got != "[a b] 9[c] 12[d] 2" {
		t.Fatalf("chunks = %s, want [a b] 9[c] 12[d] 2", got)
	}
	if len(chunks[0].spans) != 7 || len(chunks[0].interesting) != 2 || len(chunks[2].interesting) != 1 {
		t.Errorf("chunk spans: first %d staged %d interesting, last %d interesting",
			len(chunks[0].spans), len(chunks[0].interesting), len(chunks[2].interesting))
	}
	for _, s := range chunks[0].interesting {
		if s.TraceID != "a" {
			t.Errorf("first chunk carries an interesting span of trace %s", s.TraceID)
		}
	}
}

// flushOnce commits a batch in chunks of at most stagingChunkRows rows and the
// accumulator sees every span.
func TestStagingFlushCommitsInChunks(t *testing.T) {
	spans := append(append(traceSpans("err", "error", 200), traceSpans("b1", "unset", 100)...), traceSpans("b2", "unset", 150)...)
	repo := &mockStagingRepo{ready: []string{"err", "b1", "b2"}, spans: spans}
	acc := &countingAcc{}
	f := NewStagingFlusher(repo, repo, StagingFlusherConfig{SlowThresholdUs: 1000}, nil)
	f.SetAccumulator(acc)

	if _, err := f.flushOnce(context.Background()); err != nil {
		t.Fatalf("flushOnce: %v", err)
	}
	if got := fmt.Sprint(repo.committedTraces); got != "[[err] [b1 b2]]" {
		t.Errorf("committed traces = %s, want [[err] [b1 b2]]", got)
	}
	if len(repo.committed[0]) != 200 || len(repo.committed[1]) != 0 {
		t.Errorf("interesting per commit = %d and %d, want 200 and 0", len(repo.committed[0]), len(repo.committed[1]))
	}
	if acc.n != 450 {
		t.Errorf("accumulator saw %d spans, want 450", acc.n)
	}
}

// A chunk whose commit fails stays in staging and out of the accumulator, so
// the flush that later moves it counts its spans once.
func TestStagingFlushFailedChunkStaysUncounted(t *testing.T) {
	spans := append(traceSpans("b1", "unset", 200), traceSpans("b2", "unset", 200)...)
	repo := &mockStagingRepo{ready: []string{"b1", "b2"}, spans: spans, failCommit: 2}
	acc := &countingAcc{}
	f := NewStagingFlusher(repo, repo, StagingFlusherConfig{SlowThresholdUs: 1000}, nil)
	f.SetAccumulator(acc)

	if _, err := f.flushOnce(context.Background()); err == nil {
		t.Fatal("flushOnce with a failing chunk returned no error")
	}
	if acc.n != 200 {
		t.Errorf("accumulator saw %d spans, want the 200 of the committed chunk", acc.n)
	}
}
