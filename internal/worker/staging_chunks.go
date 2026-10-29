package worker

import (
	"context"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// stagingChunkRows caps the rows one staging flush job writes: the staged
// spans of its traces, which it deletes, plus their interesting spans, which it
// moves into the indexed spans table. One job of 200 traces wrote 4112 rows in
// 11.8 s on 2026-10-28, and jobs of 614 to 1378 rows took 1.7 to 4.2 ms a row.
const stagingChunkRows = 300

// stagingChunk is a set of whole traces that one CommitStagingFlush job moves,
// or one part of a trace over the cap.
type stagingChunk struct {
	traceIDs    []string
	spans       []repository.Span
	interesting []repository.Span
	rows        int
	part        *tracePart
}

// tracePart marks a chunk that moves only some staged rows of one trace.
type tracePart struct {
	key       repository.TraceKey
	stagedIDs []int64
	refresh   bool // the trace's last part, when it stores any span
}

// stagingChunks cuts a flush batch into chunks of whole traces, in the order
// of traceIDs, adding traces to a chunk until the next would take it past
// maxRows. A trace with more rows than maxRows is cut into parts of its own
// (traceParts).
func stagingChunks(traceIDs []string, spans, interesting []repository.Span, maxRows int) []stagingChunk {
	staged := spansByTrace(spans)
	kept := spansByTrace(interesting)
	var out []stagingChunk
	var cur stagingChunk
	for _, id := range traceIDs {
		rows := len(staged[id]) + len(kept[id])
		if len(cur.traceIDs) > 0 && (cur.rows+rows > maxRows || rows > maxRows) {
			out = append(out, cur)
			cur = stagingChunk{}
		}
		if rows > maxRows {
			out = append(out, traceParts(staged[id], kept[id], maxRows)...)
			continue
		}
		cur.traceIDs = append(cur.traceIDs, id)
		cur.spans = append(cur.spans, staged[id]...)
		cur.interesting = append(cur.interesting, kept[id]...)
		cur.rows += rows
	}
	if len(cur.traceIDs) > 0 {
		out = append(out, cur)
	}
	return out
}

func spansByTrace(spans []repository.Span) map[string][]repository.Span {
	out := make(map[string][]repository.Span)
	for _, s := range spans {
		out[s.TraceID] = append(out[s.TraceID], s)
	}
	return out
}

// commitChunk moves one chunk in one job, then feeds its spans to the
// accumulator and stores its prompt records. The accumulator sees a chunk only
// once it is committed, so a chunk that fails stays in staging and is counted
// when a later flush moves it, and never twice.
func (f *StagingFlusher) commitChunk(ctx context.Context, c stagingChunk) error {
	var err error
	if p := c.part; p != nil {
		err = f.repo.CommitStagingTracePart(ctx, p.key, p.stagedIDs, c.interesting, p.refresh)
	} else {
		err = f.repo.CommitStagingFlush(ctx, c.traceIDs, c.interesting)
	}
	if err != nil {
		return err
	}
	if f.accumulator != nil {
		f.accumulator.AddBatch(c.spans)
	}
	if promptRecs := extractPromptRecords(c.interesting); len(promptRecs) > 0 {
		if err := f.repo.InsertPromptRecords(promptRecs); err != nil {
			f.logger.Warn("staging flush: insert prompt records", "count", len(promptRecs), "error", err)
		}
	}
	return nil
}
