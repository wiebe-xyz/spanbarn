package main

import (
	"fmt"
	"slices"
	"time"
)

// timings collects latencies of one measured operation.
type timings []time.Duration

// pct returns the q-th percentile (0 < q <= 1), nearest-rank.
func (t timings) pct(q float64) time.Duration {
	if len(t) == 0 {
		return 0
	}
	s := slices.Clone(t)
	slices.Sort(s)
	i := int(q*float64(len(s))+0.5) - 1
	return s[min(max(i, 0), len(s)-1)]
}

func (t timings) String() string {
	return fmt.Sprintf("p50=%s p95=%s p99=%s n=%d", ms(t.pct(0.50)), ms(t.pct(0.95)), ms(t.pct(0.99)), len(t))
}

// measure runs fn n times and records each run's wall time.
func measure(n int, fn func() error) (timings, error) {
	out := make(timings, 0, n)
	for range n {
		start := time.Now()
		if err := fn(); err != nil {
			return out, err
		}
		out = append(out, time.Since(start))
	}
	return out, nil
}

// ms formats a duration in milliseconds with two decimals, for result tables.
func ms(d time.Duration) string { return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000) }
