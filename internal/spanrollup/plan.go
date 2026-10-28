package spanrollup

import (
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/rollup"
)

// coarseFirst lists the tiers a plan may read whole buckets from, coarsest
// first. The 5-minute tier fills whatever they leave.
var coarseFirst = []int64{rollup.StepDay, rollup.StepHour}

// Plan picks the tier segments that cover [from, to): whole days and hours
// where those tiers hold them, 5-minute buckets for the rest. The range
// starts at the 5-minute bucket that holds from and ends with the bucket that
// holds to, so the edges read whole buckets.
//
// align is the width of the caller's chart buckets in seconds. A tier is used
// only when align is a multiple of its step, so no tier bucket straddles two
// chart buckets; 0 means one total and allows every tier.
//
// ok is false when the tiers do not hold the whole range: part of it is older
// than the oldest bucket a usable tier keeps, or align is not a multiple of
// 5 minutes. Plan does not see a gap inside a tier; Complete checks that
// every bucket of the plan holds rows. The caller then reads its other source for the whole range,
// because two sources are never stitched into one result.
func Plan(from, to time.Time, align int64, cov repository.SpanRollupCoverage) ([]repository.SpanRollupSegment, bool) {
	if !to.After(from) || !aligned(align, rollup.Step5m) {
		return nil, false
	}
	var segs []repository.SpanRollupSegment
	cur := rollup.TruncateBucket(from, rollup.Step5m)
	for cur.Before(to) {
		if seg, ok := coarseSegment(cur, to, align, cov); ok {
			segs = append(segs, seg)
			cur = seg.To
			continue
		}
		oldest, ok := cov.Oldest[rollup.Step5m]
		if !ok || cur.Before(oldest) {
			return nil, false
		}
		next := rollup.NextBucket(cur, rollup.Step5m)
		if n := len(segs); n > 0 && segs[n-1].Step == rollup.Step5m && segs[n-1].To.Equal(cur) {
			segs[n-1].To = next
		} else {
			segs = append(segs, repository.SpanRollupSegment{Step: rollup.Step5m, From: cur, To: next})
		}
		cur = next
	}
	return segs, true
}

// coarseSegment returns the longest run of whole buckets of the coarsest tier
// that starts at cur, ends by to and is held by that tier.
func coarseSegment(cur, to time.Time, align int64, cov repository.SpanRollupCoverage) (repository.SpanRollupSegment, bool) {
	for _, step := range coarseFirst {
		if !aligned(align, step) || !rollup.TruncateBucket(cur, step).Equal(cur) {
			continue
		}
		// The oldest bucket of a coarse tier can be partial: it was built
		// from the first source rows, which may start inside it.
		oldest, ok := cov.Oldest[step]
		if !ok || !cur.After(oldest) {
			continue
		}
		// A tier holds the buckets its source tier was compacted past.
		through, ok := cov.Through[sourceOf(step)]
		if !ok {
			continue
		}
		end := cur
		for next := rollup.NextBucket(end, step); !next.After(to) && !next.After(through); next = rollup.NextBucket(end, step) {
			end = next
		}
		if end.After(cur) {
			return repository.SpanRollupSegment{Step: step, From: cur, To: end}, true
		}
	}
	return repository.SpanRollupSegment{}, false
}

func sourceOf(step int64) int64 {
	for _, t := range Ladder() {
		if t.Step == step {
			return t.Source
		}
	}
	return 0
}

func aligned(align, step int64) bool {
	return align == 0 || (align >= step && align%step == 0)
}
