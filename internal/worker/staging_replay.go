package worker

import "github.com/wiebe-xyz/spanbarn/internal/repository"

// The write queue delivers at least once: a batch reclaimed from the processing
// list can reach spans_staging a second time. No span table has a unique key on
// trace and span id, and aggregates go through the accumulator, so the flusher
// drops the second copy here, where every staged row of a trace is in hand. A
// copy that lands after the first has already been flushed gets past this.

// dropReplayedSpans returns one staged row per project, trace and span id, plus
// the staging ids of the rows it dropped, keyed by the staging id of the row
// kept. A span with no span id has nothing to compare and is always kept.
func dropReplayedSpans(spans []repository.Span) ([]repository.Span, map[int64][]int64) {
	type spanKey struct {
		projectID       int64
		traceID, spanID string
	}
	kept := make(map[spanKey]int64, len(spans))
	unique := make([]repository.Span, 0, len(spans))
	var replayed map[int64][]int64
	for _, s := range spans {
		if s.SpanID == "" {
			unique = append(unique, s)
			continue
		}
		k := spanKey{s.ProjectID, s.TraceID, s.SpanID}
		first, seen := kept[k]
		if !seen {
			kept[k] = s.ID
			unique = append(unique, s)
			continue
		}
		if replayed == nil {
			replayed = make(map[int64][]int64)
		}
		replayed[first] = append(replayed[first], s.ID)
	}
	return unique, replayed
}

// addReplayedRows adds dropped rows to the part holding the copy that was kept,
// so they leave staging in the same commit. A part names its rows by staging id;
// a whole-trace chunk deletes by trace id and needs nothing.
func (c *stagingChunk) addReplayedRows(replayed map[int64][]int64) {
	if c.part == nil || len(replayed) == 0 {
		return
	}
	for _, s := range c.spans {
		c.part.stagedIDs = append(c.part.stagedIDs, replayed[s.ID]...)
	}
}
