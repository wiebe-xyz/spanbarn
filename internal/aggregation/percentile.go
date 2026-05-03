package aggregation

import "sort"

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

// P50 returns the 50th percentile (median).
func P50(values []int64) int64 { return Compute(values, 50) }

// P95 returns the 95th percentile.
func P95(values []int64) int64 { return Compute(values, 95) }

// P99 returns the 99th percentile.
func P99(values []int64) int64 { return Compute(values, 99) }
