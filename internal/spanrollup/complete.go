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

// BucketCounterFunc adapts a bucket count of another tiered table, such as
// CountSpanEdgeBuckets, to BucketCounter.
type BucketCounterFunc func(ctx context.Context, step int64, from, to time.Time) (int, error)

// CountSpanRollupBuckets calls f.
func (f BucketCounterFunc) CountSpanRollupBuckets(ctx context.Context, step int64, from, to time.Time) (int, error) {
	return f(ctx, step, from, to)
}

// SpanArrivals tells a quiet bucket from a gap: it reports whether any span
// was ingested in [from, to).
type SpanArrivals interface {
	SpansIngestedBetween(ctx context.Context, from, to time.Time) (bool, error)
}

// Arrivals reports an arrival that either source saw: a 5-minute span_rollups
// bucket that holds rows, or a raw span. span_edges asks it about its own
// empty buckets, and a span_rollups row beside one marks it as a gap even
// after retention removed the raw spans.
func Arrivals(rollups BucketCounter, raw SpanArrivals) SpanArrivals {
	return arrivalsFunc(func(ctx context.Context, from, to time.Time) (bool, error) {
		n, err := rollups.CountSpanRollupBuckets(ctx, rollup.Step5m, from, to)
		if err != nil || n > 0 {
			return n > 0, err
		}
		return raw.SpansIngestedBetween(ctx, from, to)
	})
}

type arrivalsFunc func(ctx context.Context, from, to time.Time) (bool, error)

func (f arrivalsFunc) SpansIngestedBetween(ctx context.Context, from, to time.Time) (bool, error) {
	return f(ctx, from, to)
}

// maxQuietBuckets bounds the empty 5-minute buckets one segment may hold and
// still count as complete. Each one costs a lookup in spans, and a long run of
// them is more likely rollups that were off than ingest that was quiet.
const maxQuietBuckets = 32

// Complete reports whether every bucket the segments read holds rows. A
// bucket without rows is a gap when spans arrived in it: no writer rolled
// them up then (an older version ran, or the rollups were off), so a read
// across it would count short. Plan cannot see such a gap, because it knows
// only where each tier starts and how far it was compacted.
//
// A 5-minute bucket in which no span arrived at all, which happens while a
// deploy rolls the ingest pods, is empty in the spans table as well, so it is
// quiet and counts as covered. Once retention has removed a window's raw
// spans the rollups are the only record of it, and their buckets count as
// covered too. arrivals may be nil, and every empty bucket is then a gap.
//
// Buckets that start at or after open are not checked: the writer has not
// flushed them yet.
func Complete(ctx context.Context, c BucketCounter, arrivals SpanArrivals, segs []repository.SpanRollupSegment, open time.Time) (bool, error) {
	for _, seg := range segs {
		if ok, err := segmentCovered(ctx, c, arrivals, seg, open); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// segmentCovered reports whether every bucket of seg that starts before open
// holds rows or, in the 5-minute tier, is quiet.
func segmentCovered(ctx context.Context, c BucketCounter, arrivals SpanArrivals, seg repository.SpanRollupSegment, open time.Time) (bool, error) {
	want, end := bucketsBefore(seg, open)
	if want == 0 {
		return true, nil
	}
	got, err := c.CountSpanRollupBuckets(ctx, seg.Step, seg.From, end)
	if err != nil || got >= want {
		return err == nil, err
	}
	if seg.Step != rollup.Step5m || arrivals == nil || want-got > maxQuietBuckets {
		return false, nil
	}
	return emptyBucketsQuiet(ctx, c, arrivals, repository.SpanRollupSegment{Step: seg.Step, From: seg.From, To: end})
}

// emptyBucketsQuiet reports whether no span arrived in any bucket of seg that
// holds no rows.
func emptyBucketsQuiet(ctx context.Context, c BucketCounter, arrivals SpanArrivals, seg repository.SpanRollupSegment) (bool, error) {
	for b := seg.From; b.Before(seg.To); b = rollup.NextBucket(b, seg.Step) {
		next := rollup.NextBucket(b, seg.Step)
		n, err := c.CountSpanRollupBuckets(ctx, seg.Step, b, next)
		if err != nil {
			return false, err
		}
		if n > 0 {
			continue
		}
		if arrived, err := arrivals.SpansIngestedBetween(ctx, b, next); err != nil || arrived {
			return false, err
		}
	}
	return true, nil
}

// bucketsBefore counts the buckets of seg that start before open, and returns
// the end of the last of them.
func bucketsBefore(seg repository.SpanRollupSegment, open time.Time) (int, time.Time) {
	n, end := 0, seg.From
	for b := seg.From; b.Before(seg.To) && b.Before(open); b = rollup.NextBucket(b, seg.Step) {
		n++
		end = rollup.NextBucket(b, seg.Step)
	}
	return n, end
}

// windowComplete reports whether every source bucket of [start, end) holds
// rows or is quiet, so a coarse bucket built from them counts every span of
// its window.
func windowComplete(ctx context.Context, c BucketCounter, arrivals SpanArrivals, step int64, start, end time.Time) (bool, error) {
	return segmentCovered(ctx, c, arrivals, repository.SpanRollupSegment{Step: step, From: start, To: end}, end)
}
