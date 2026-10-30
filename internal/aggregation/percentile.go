package aggregation

import (
	"sort"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Compute returns the given percentile (0-100) of the values slice.
// It uses exact sorted computation: sort the slice and pick the index.
// Returns 0 for an empty slice.
func Compute(values []int64, percentile float64) int64 {
	n := len(values)
	if n == 0 {
		return 0
	}

	// Copy to avoid mutating the caller's slice.
	sorted := make([]int64, n)
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	if n == 1 {
		return sorted[0]
	}

	// Nearest-rank method: index = ceil(percentile/100 * n) - 1, clamped.
	idx := int(percentile/100.0*float64(n)+0.5) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

// ComputeWeighted returns the given percentile (0-100) of values when value i
// stands for weights[i] observations. The trace buffer keeps every slow and
// error trace and samples the rest, so the retained durations lean slow; the
// weights restore the population they were drawn from. Returns 0 for an empty
// slice.
func ComputeWeighted(values, weights []int64, percentile float64) int64 {
	return repository.WeightedPercentile(values, weights, percentile)
}

// percentilesOf returns p50, p95 and p99 of durations. When every span carries a
// sample weight the percentiles are weighted by it; a weight of 0 means unknown
// and the whole set reads unweighted, as the weighted counts do.
func percentilesOf(durations, weights []int64) (p50, p95, p99 int64) {
	if len(weights) == len(durations) && repository.FullyWeighted(weights) {
		return ComputeWeighted(durations, weights, 50),
			ComputeWeighted(durations, weights, 95),
			ComputeWeighted(durations, weights, 99)
	}
	return P50(durations), P95(durations), P99(durations)
}

// P50 returns the 50th percentile (median).
func P50(values []int64) int64 { return Compute(values, 50) }

// P95 returns the 95th percentile.
func P95(values []int64) int64 { return Compute(values, 95) }

// P99 returns the 99th percentile.
func P99(values []int64) int64 { return Compute(values, 99) }
