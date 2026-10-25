// Package spanhist folds span durations into quarter-octave histograms and
// reads percentiles back from them.
//
// A duration d (microseconds) falls in bin floor(4*log2(d+1)), so each bin is
// a factor of 2^(1/4) (about 19%) wide and 160 bins reach past 12 days. The
// heatmap's half-octave bucket of the same duration is bin>>1.
//
// SQL folds rows with IndexSQL and returns one Bin per group and bin index,
// with the bin's count and the smallest and largest duration in it. A
// percentile interpolates between those two, so a value lands within its
// bin's observed range: exact when the bin holds evenly spaced values, and
// never further off than the bin's width.
package spanhist

import (
	"math"
	"slices"
)

// Bins is the number of quarter-octave bins. The last one holds everything
// longer than 2^(159/4) microseconds.
const Bins = 160

// IndexSQL returns the SQL expression that assigns the duration in column col
// to its bin. Negative durations fall in bin 0, as in Index. The caller clamps
// the result with Clamp.
func IndexSQL(col string) string {
	return "CAST(log2(MAX(" + col + ", 0) + 1) * 4 AS INTEGER)"
}

// Index returns the bin of a duration in microseconds.
func Index(us int64) int {
	if us <= 0 {
		return 0
	}
	return Clamp(int(math.Log2(float64(us)+1) * 4))
}

// Clamp keeps a bin index read from SQL inside 0..Bins-1.
func Clamp(i int) int {
	return min(max(i, 0), Bins-1)
}

// Bin is one histogram bin: how many durations fell in it and the smallest
// and largest of them.
type Bin struct {
	Index    int
	Count    int64
	Min, Max int64
}

// Hist is a sparse quarter-octave histogram. The zero value is empty.
type Hist struct {
	bins  []Bin // ascending Index, one per index
	total int64
}

// Add folds b into h, merging it with a bin of the same index.
func (h *Hist) Add(b Bin) {
	if b.Count <= 0 {
		return
	}
	b.Index = Clamp(b.Index)
	h.total += b.Count
	i, found := slices.BinarySearchFunc(h.bins, b.Index, func(x Bin, idx int) int { return x.Index - idx })
	if !found {
		h.bins = slices.Insert(h.bins, i, b)
		return
	}
	cur := &h.bins[i]
	cur.Count += b.Count
	cur.Min = min(cur.Min, b.Min)
	cur.Max = max(cur.Max, b.Max)
}

// Merge folds every bin of o into h.
func (h *Hist) Merge(o Hist) {
	for _, b := range o.bins {
		h.Add(b)
	}
}

// Count returns the number of durations in h.
func (h Hist) Count() int64 { return h.total }

// Percentile returns the p-th percentile (0 < p <= 100) by nearest rank,
// round(p/100*n), interpolated inside the bin that holds that rank. An empty
// histogram returns 0.
func (h Hist) Percentile(p float64) int64 {
	if h.total == 0 {
		return 0
	}
	rank := min(max(int64(p/100*float64(h.total)+0.5), 1), h.total)
	var before int64
	for _, b := range h.bins {
		if before+b.Count < rank {
			before += b.Count
			continue
		}
		if b.Count == 1 {
			return b.Min
		}
		pos := float64(rank-before-1) / float64(b.Count-1)
		return b.Min + int64(math.Round(pos*float64(b.Max-b.Min)))
	}
	return h.bins[len(h.bins)-1].Max
}
