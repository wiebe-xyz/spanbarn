package spanhist

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// fold builds a histogram from raw durations the way SQL does: one Bin per
// index with its count, min and max.
func fold(durations []int64) Hist {
	var h Hist
	for _, d := range durations {
		h.Add(Bin{Index: Index(d), Count: 1, Min: d, Max: d})
	}
	return h
}

func nearestRank(sorted []int64, p float64) int64 {
	rank := min(max(int(p/100*float64(len(sorted))+0.5), 1), len(sorted))
	return sorted[rank-1]
}

func TestIndexEdges(t *testing.T) {
	for _, tc := range []struct {
		us   int64
		want int
	}{
		{-5, 0}, {0, 0}, {1, 4}, {3, 8}, {7, 12}, {1023, 40}, {math.MaxInt64, Bins - 1},
	} {
		if got := Index(tc.us); got != tc.want {
			t.Errorf("Index(%d) = %d, want %d", tc.us, got, tc.want)
		}
	}
	if Index(1)>>1 != 2 {
		t.Error("half-octave bucket of 1us must be the heatmap's bucket 2")
	}
}

// Evenly spaced values in a bin interpolate to the exact nearest-rank value.
func TestPercentileExactOnEvenSpacing(t *testing.T) {
	var d []int64
	for i := int64(1); i <= 100; i++ {
		d = append(d, i*1000)
	}
	h := fold(d)
	for _, p := range []float64{50, 90, 95, 99, 100} {
		if got, want := h.Percentile(p), nearestRank(d, p); got != want {
			t.Errorf("p%v = %d, want %d", p, got, want)
		}
	}
}

// On a long-tailed sample the estimate stays inside its bin: within a factor
// of 2^(1/4) of the exact value.
func TestPercentileWithinOneBin(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	d := make([]int64, 20000)
	for i := range d {
		d[i] = int64(math.Exp(r.NormFloat64()*1.5 + 9))
	}
	h := fold(d)
	slices.Sort(d)
	limit := math.Pow(2, 0.25)
	for _, p := range []float64{50, 90, 95, 99, 99.9} {
		got, want := float64(h.Percentile(p)), float64(nearestRank(d, p))
		if ratio := max(got, want) / max(min(got, want), 1); ratio > limit {
			t.Errorf("p%v = %v, exact %v: off by %.3fx", p, got, want, ratio)
		}
	}
}

// Merging is order independent: two halves merged either way equal one fold.
func TestMergeIsOrderIndependent(t *testing.T) {
	a, b := []int64{5, 900, 12000, 12500}, []int64{7, 880, 13000, 2_000_000}
	ha, hb := fold(a), fold(b)
	ab, ba := ha, hb
	ab.bins = slices.Clone(ha.bins)
	ba.bins = slices.Clone(hb.bins)
	ab.Merge(hb)
	ba.Merge(ha)
	whole := fold(append(slices.Clone(a), b...))
	if !slices.Equal(ab.bins, whole.bins) || !slices.Equal(ba.bins, whole.bins) || ab.Count() != 8 {
		t.Errorf("merge a+b %v, b+a %v, whole %v", ab.bins, ba.bins, whole.bins)
	}
}

func TestEmptyAndSingle(t *testing.T) {
	var h Hist
	if h.Percentile(99) != 0 || h.Count() != 0 {
		t.Error("empty histogram must read 0")
	}
	h.Add(Bin{Index: Index(42), Count: 1, Min: 42, Max: 42})
	h.Add(Bin{Index: 3, Count: 0})
	if h.Percentile(50) != 42 || h.Percentile(100) != 42 || h.Count() != 1 {
		t.Errorf("single = %d/%d, count %d", h.Percentile(50), h.Percentile(100), h.Count())
	}
}
