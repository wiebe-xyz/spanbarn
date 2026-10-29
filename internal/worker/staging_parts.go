package worker

import (
	"cmp"
	"slices"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// A trace with more rows than the chunk cap used to move in one job of its
// own. On prod (2026-10-29) traces of 2,400 to 9,200 rows held the spans
// writer for 4 to 21 s. traceParts cuts such a trace into parts of at most
// maxRows rows: a part names its staged rows by staging id, so it deletes
// only those and moves only their interesting spans.
//
// A part that fails leaves its rows, and every later part's rows, in staging.
// The next flush finds the trace ready again and classifies what is left; the
// parts already moved stay stored and counted.

// traceParts cuts one trace's staged spans, in staging id order, into parts
// of at most maxRows rows (a staged row, plus its interesting copy when it has
// one). The last part refreshes the trace's structure when the trace stores
// any span.
func traceParts(staged, kept []repository.Span, maxRows int) []stagingChunk {
	staged = slices.SortedFunc(slices.Values(staged), func(a, b repository.Span) int { return cmp.Compare(a.ID, b.ID) })
	keptByID := make(map[int64]repository.Span, len(kept))
	for _, s := range kept {
		keptByID[s.ID] = s
	}
	var out []stagingChunk
	cur := newTracePart(staged[0])
	for _, s := range staged {
		k, ok := keptByID[s.ID]
		rows := 1
		if ok {
			rows = 2
		}
		if len(cur.part.stagedIDs) > 0 && cur.rows+rows > maxRows {
			out = append(out, cur)
			cur = newTracePart(s)
		}
		cur.spans = append(cur.spans, s)
		cur.part.stagedIDs = append(cur.part.stagedIDs, s.ID)
		cur.rows += rows
		if ok {
			cur.interesting = append(cur.interesting, k)
			delete(keptByID, s.ID)
		}
	}
	// An interesting span with no staged row of its own still moves, with the
	// last part.
	for _, s := range kept {
		if _, left := keptByID[s.ID]; left {
			cur.interesting = append(cur.interesting, s)
			cur.rows++
		}
	}
	cur.part.refresh = len(kept) > 0
	return append(out, cur)
}

func newTracePart(s repository.Span) stagingChunk {
	return stagingChunk{
		traceIDs: []string{s.TraceID},
		part:     &tracePart{key: repository.TraceKey{ProjectID: s.ProjectID, TraceID: s.TraceID}},
	}
}
