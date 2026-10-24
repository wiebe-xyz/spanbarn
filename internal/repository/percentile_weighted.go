package repository

import (
	"math"
	"sort"
)

// WeightedPercentile returns the given percentile (0-100) of values when value i
// stands for weights[i] observations. The trace buffer keeps every slow and
// error trace and samples the rest, so the retained durations lean slow; the
// weights restore the population they were drawn from. Returns 0 for an empty
// slice or when the slices differ in length.
func WeightedPercentile(values, weights []int64, percentile float64) int64 {
	n := len(values)
	if n == 0 || len(weights) != n {
		return 0
	}
	idx := make([]int, n)
	var total int64
	for i := range idx {
		idx[i] = i
		total += weights[i]
	}
	sort.Slice(idx, func(a, b int) bool { return values[idx[a]] < values[idx[b]] })

	// The first value whose cumulative weight reaches the percentile's share.
	target := int64(math.Ceil(percentile / 100.0 * float64(total)))
	var cum int64
	for _, i := range idx {
		cum += weights[i]
		if cum >= target {
			return values[i]
		}
	}
	return values[idx[n-1]]
}

// FullyWeighted reports whether every span of a set carries a sample weight.
// A weight of 0 means unknown, and a set with an unknown weight reads
// unweighted, as the weighted counts do.
func FullyWeighted(weights []int64) bool {
	if len(weights) == 0 {
		return false
	}
	for _, w := range weights {
		if w <= 0 {
			return false
		}
	}
	return true
}
