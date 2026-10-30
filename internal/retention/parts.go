package retention

import (
	"cmp"
	"context"
	"slices"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// retentionPart is how many spans of a retention batch one sample copy and one
// span delete cover, so no job holds the spans writer for a whole 5000-span
// batch.
const retentionPart = 1000

// sampleAndDeleteInParts runs sampleAndDelete over the batch in parts of
// retentionPart spans, in id order. A stop between parts leaves the later parts'
// spans for the next cycle.
//
// A part deletes every span up to its highest id, as the whole batch did; the
// last part ends at the batch's highest id, so the parts delete what the batch
// did.
func (w *RetentionWorker) sampleAndDeleteInParts(ctx context.Context, cfg Config, batch []repository.Span, st *cycleStats) error {
	for _, part := range spanParts(batch, retentionPart) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.sampleAndDelete(ctx, cfg, part, st); err != nil {
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
