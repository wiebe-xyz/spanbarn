package worker

import (
	"context"
	"fmt"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// stagedID numbers staged spans the way spans_staging's id column does.
var stagedID int64

func traceSpans(traceID, status string, n int) []repository.Span {
	out := make([]repository.Span, n)
	for i := range out {
		stagedID++
		out[i] = repository.Span{ID: stagedID, TraceID: traceID, SpanID: fmt.Sprintf("%s-%d", traceID, i), Status: status, DurationUs: 1}
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
// cap is cut into parts of its own, and each chunk carries only its own spans.
func TestStagingChunksSplitsWholeTraces(t *testing.T) {
	a, b, c, d := traceSpans("a", "unset", 4), traceSpans("b", "unset", 3), traceSpans("c", "unset", 12), traceSpans("d", "unset", 1)
	spans := append(append(append(append([]repository.Span{}, a...), b...), c...), d...)
	interesting := append(append([]repository.Span{}, a[:2]...), d...)

	chunks := stagingChunks([]string{"a", "b", "c", "d"}, spans, interesting, 10)
	if got := chunkTraces(chunks); got != "[a b] 9[c] 10[c] 2[d] 2" {
		t.Fatalf("chunks = %s, want [a b] 9[c] 10[c] 2[d] 2", got)
	}
	if len(chunks[0].spans) != 7 || len(chunks[0].interesting) != 2 || len(chunks[3].interesting) != 1 {
		t.Errorf("chunk spans: first %d staged %d interesting, last %d interesting",
			len(chunks[0].spans), len(chunks[0].interesting), len(chunks[3].interesting))
	}
	if chunks[0].part != nil || chunks[1].part == nil || chunks[2].part == nil || chunks[3].part != nil {
		t.Error("only the chunks of trace c should be trace parts")
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
	// err is 400 rows (200 staged, 200 stored), so it moves in two parts.
	if got := fmt.Sprint(repo.committedTraces); got != "[[err] [err] [b1 b2]]" {
		t.Errorf("committed traces = %s, want [[err] [err] [b1 b2]]", got)
	}
	if got := fmt.Sprint(len(repo.committed[0]), len(repo.committed[1]), len(repo.committed[2])); got != "150 50 0" {
		t.Errorf("interesting per commit = %s, want 150 50 0", got)
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

// A trace over the cap moves in parts of at most the cap: each part names its
// staged rows and carries the interesting copies of those rows, every staged
// row is in exactly one part, and only the last part refreshes the trace.
func TestTracePartsCutAnOverCapTrace(t *testing.T) {
	staged := traceSpans("big", "error", 25)
	kept := append([]repository.Span{}, staged[:20]...) // 20 rows stored, 5 dropped

	parts := traceParts(staged, kept, 10)
	seen := map[int64]int{}
	var interesting int
	for i, p := range parts {
		if p.rows > 10 || p.part == nil {
			t.Fatalf("part %d: %d rows, part %v", i, p.rows, p.part)
		}
		if p.part.refresh != (i == len(parts)-1) {
			t.Errorf("part %d refresh = %v", i, p.part.refresh)
		}
		ids := map[int64]bool{}
		for _, id := range p.part.stagedIDs {
			seen[id]++
			ids[id] = true
		}
		for _, s := range p.interesting {
			if !ids[s.ID] {
				t.Errorf("part %d moves interesting span %d without its staged row", i, s.ID)
			}
		}
		interesting += len(p.interesting)
	}
	if len(seen) != 25 || interesting != 20 {
		t.Fatalf("parts cover %d staged rows and %d interesting spans, want 25 and 20", len(seen), interesting)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("staged row %d is in %d parts", id, n)
		}
	}
	if len(parts) != 5 {
		t.Errorf("%d parts, want 5 (four of 5 stored rows, one of 5 dropped rows)", len(parts))
	}
}

// flushOnce moves a trace over the cap in parts and the accumulator still sees
// each span once.
func TestStagingFlushMovesAnOverCapTraceInParts(t *testing.T) {
	spans := traceSpans("big", "error", 700)
	repo := &mockStagingRepo{ready: []string{"big"}, spans: spans}
	acc := &countingAcc{}
	f := NewStagingFlusher(repo, repo, StagingFlusherConfig{SlowThresholdUs: 1000}, nil)
	f.SetAccumulator(acc)

	if _, err := f.flushOnce(context.Background()); err != nil {
		t.Fatalf("flushOnce: %v", err)
	}
	if len(repo.parts) != 5 {
		t.Fatalf("%d part commits, want 5 (1400 rows at 300 a part)", len(repo.parts))
	}
	var staged, kept int
	for i, p := range repo.parts {
		staged += p.staged
		kept += p.interesting
		if p.refresh != (i == len(repo.parts)-1) {
			t.Errorf("part %d refresh = %v", i, p.refresh)
		}
	}
	if staged != 700 || kept != 700 || acc.n != 700 {
		t.Errorf("parts moved %d staged and %d interesting, accumulator saw %d; want 700 each", staged, kept, acc.n)
	}
}
