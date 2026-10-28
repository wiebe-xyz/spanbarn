package spanrollup

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/rollup"
)

// BucketCounter counts the buckets of a tier that hold rows.
type BucketCounter interface {
	CountSpanRollupBuckets(ctx context.Context, step int64, from, to time.Time) (int, error)
}

// Complete reports whether every bucket the segments read holds rows. A
// bucket without rows is a gap: no writer rolled spans up then (an older
// version ran, or the rollups were off), so a read across it would count
// short. Plan cannot see such a gap, because it knows only where each tier
// starts and how far it was compacted.
//
// Buckets that start at or after open are not checked: the writer has not
// flushed them yet. A bucket in which no project received a single span also
// reads as a gap, and the caller then reads its other source.
func Complete(ctx context.Context, c BucketCounter, segs []repository.SpanRollupSegment, open time.Time) (bool, error) {
	for _, seg := range segs {
		want := bucketsBefore(seg, open)
		if want == 0 {
			continue
		}
		got, err := c.CountSpanRollupBuckets(ctx, seg.Step, seg.From, seg.To)
		if err != nil {
			return false, err
		}
		if got < want {
			return false, nil
		}
	}
	return true, nil
}

// bucketsBefore counts the buckets of seg that start before open.
func bucketsBefore(seg repository.SpanRollupSegment, open time.Time) int {
	n := 0
	for b := seg.From; b.Before(seg.To) && b.Before(open); b = rollup.NextBucket(b, seg.Step) {
		n++
	}
	return n
}

// windowComplete reports whether every source bucket of [start, end) holds
// rows, so a coarse bucket built from them counts every span of its window.
func windowComplete(ctx context.Context, c BucketCounter, step int64, start, end time.Time) (bool, error) {
	seg := repository.SpanRollupSegment{Step: step, From: start, To: end}
	got, err := c.CountSpanRollupBuckets(ctx, step, start, end)
	return err == nil && got >= bucketsBefore(seg, end), err
}
