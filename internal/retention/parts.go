package retention

import (
	"cmp"
	"context"
	"slices"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// retentionPart is how many spans of a retention batch one aggregate write
// and one span delete cover. One persist of a whole 5000-span batch wrote 300
// to 580 aggregate rows at about 5.5 ms a row and once held the spans writer
// for 20 s (2026-10-28). A part of 1000 spans wrote 76 to 150 rows on prod.
const retentionPart = 1000

// aggregateAndDeleteInParts runs aggregateAndDelete over the batch in parts of
// retentionPart spans, in id order. Each part persists its own aggregates and
// then deletes its own spans, so a stop between parts leaves only unaggregated
// spans behind and the next cycle counts nothing twice. The aggregate upsert
// adds counts, which is why it is never split inside one part.
//
// A part deletes every span up to its highest id, as the whole batch did; the
// last part ends at the batch's highest id, so the parts delete what the batch
// did.
func (w *RetentionWorker) aggregateAndDeleteInParts(ctx context.Context, cfg Config, batch []repository.Span, st *cycleStats) error {
	for _, part := range spanParts(batch, retentionPart) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.aggregateAndDelete(ctx, cfg, part, st); err != nil {
			return err
		}
	}
	return nil
}

// spanParts sorts a copy of batch by id and cuts it into parts of size spans.
func spanParts(batch []repository.Span, size int) [][]repository.Span {
	sorted := slices.Clone(batch)
	slices.SortFunc(sorted, func(a, b repository.Span) int { return cmp.Compare(a.ID, b.ID) })
	return slices.Collect(slices.Chunk(sorted, size))
}
