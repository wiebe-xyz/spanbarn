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

// stagingChunk is a set of whole traces that one CommitStagingFlush job moves.
type stagingChunk struct {
	traceIDs    []string
	spans       []repository.Span
	interesting []repository.Span
	rows        int
}

// stagingChunks cuts a flush batch into chunks of whole traces, in the order
// of traceIDs, adding traces to a chunk until the next would take it past
// maxRows. A trace with more rows than maxRows gets a chunk of its own.
func stagingChunks(traceIDs []string, spans, interesting []repository.Span, maxRows int) []stagingChunk {
	staged := spansByTrace(spans)
	kept := spansByTrace(interesting)
	var out []stagingChunk
	var cur stagingChunk
	for _, id := range traceIDs {
		rows := len(staged[id]) + len(kept[id])
		if len(cur.traceIDs) > 0 && cur.rows+rows > maxRows {
			out = append(out, cur)
			cur = stagingChunk{}
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
	if len(c.traceIDs) == 1 && c.rows > stagingChunkRows {
		f.logger.Info("staging flush: trace over the chunk cap moves in one job",
			"trace_id", c.traceIDs[0], "rows", c.rows, "cap", stagingChunkRows)
	}
	if err := f.repo.CommitStagingFlush(ctx, c.traceIDs, c.interesting); err != nil {
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
