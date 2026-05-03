package aggregation

import (
	"testing"
)

func TestP50(t *testing.T) {
	// Odd-length slice: median is the middle value.
	values := []int64{10, 20, 30, 40, 50}
	got := P50(values)
	if got != 30 {
		t.Errorf("P50(%v) = %d, want 30", values, got)
	}
}

func TestP95(t *testing.T) {
	// 20 values: 1..20. P95 index = ceil(0.95*20)-1 = 18 => value 19.
	values := make([]int64, 20)
	for i := range values {
		values[i] = int64(i + 1)
	}
	got := P95(values)
	if got != 19 {
		t.Errorf("P95(1..20) = %d, want 19", got)
	}
}

func TestP99(t *testing.T) {
	// 100 values: 1..100. P99 index = ceil(0.99*100)-1 = 98 => value 99.
	values := make([]int64, 100)
	for i := range values {
		values[i] = int64(i + 1)
	}
	got := P99(values)
	if got != 99 {
		t.Errorf("P99(1..100) = %d, want 99", got)
	}
}

func TestPercentileSingleValue(t *testing.T) {
	values := []int64{42}
	for _, p := range []float64{50, 95, 99} {
		got := Compute(values, p)
		if got != 42 {
			t.Errorf("Compute([42], %.0f) = %d, want 42", p, got)
		}
	}
}

func TestPercentileEmpty(t *testing.T) {
	got := Compute(nil, 50)
	if got != 0 {
		t.Errorf("Compute(nil, 50) = %d, want 0", got)
	}
	got = Compute([]int64{}, 95)
	if got != 0 {
		t.Errorf("Compute([], 95) = %d, want 0", got)
	}
}

func TestPercentileLargeDataset(t *testing.T) {
	const n = 100_000
	values := make([]int64, n)
	for i := range values {
		values[i] = int64(i + 1) // 1..100000
	}

	p50 := P50(values)
	if p50 < 49_000 || p50 > 51_000 {
		t.Errorf("P50 of 1..100k = %d, expected ~50000", p50)
	}

	p95 := P95(values)
	if p95 < 94_000 || p95 > 96_000 {
		t.Errorf("P95 of 1..100k = %d, expected ~95000", p95)
	}

	p99 := P99(values)
	if p99 < 98_000 || p99 > 100_000 {
		t.Errorf("P99 of 1..100k = %d, expected ~99000", p99)
	}
}
