package metrics

import "sort"

// minRebounds is how many rebounds make a cumulative series interleaved. One can
// be a low counter restarting and catching up within a single interval; a
// restart that repeats that twice in a window has almost nothing to lose from
// the undercount below.
const minRebounds = 2

// Interleaved reports whether a run of cumulative values falls and climbs back
// to where it was: 11, 1, 11, 1. Pods that send a counter with no
// service.instance.id share one label set, so their running totals alternate on
// one series. A real reset stays low, because a restarted process needs its own
// lifetime to pass its old total again. A non-monotonic sum has this shape too;
// the two cannot be told apart, since monotonicity is not stored.
func Interleaved(values []float64) bool {
	rebounds := 0
	for i := 2; i < len(values); i++ {
		if values[i-1] < values[i-2] && values[i] >= values[i-2] {
			rebounds++
			if rebounds >= minRebounds {
				return true
			}
		}
	}
	return false
}

// ChainPrevious splits interleaved values into one rising chain per producer and
// returns, for each value, the index of the value before it on its chain, or -1
// when it starts a chain. A value joins the chain whose last value is the
// largest one not above it. The two pods cannot be told apart, so a pairing can
// cross between them; the sum of the steps still comes out right, because each
// step moves one chain's last value and only the last values count.
func ChainPrevious(values []float64) []int {
	prev := make([]int, len(values))
	var lasts []int
	for i, v := range values {
		best := -1
		for c, last := range lasts {
			if values[last] <= v && (best < 0 || values[last] > values[lasts[best]]) {
				best = c
			}
		}
		if best < 0 {
			startChain(values, prev, i)
			lasts = append(lasts, i)
			continue
		}
		prev[i] = lasts[best]
		lasts[best] = i
	}
	return prev
}

// startChain handles a value below every chain. Its producer may have reported
// before: an earlier step that jumped over this value was a higher pod appearing
// on a lower pod's chain (40, 8000, 40). The value takes that step's place on
// the chain and the higher value starts its own, which may in turn have jumped
// an older step. With no such step this is a producer's first value.
func startChain(values []float64, prev []int, i int) {
	for {
		j := widestStepOver(values, prev, i)
		if j < 0 {
			prev[i] = -1
			return
		}
		prev[i] = prev[j]
		i = j
	}
}

// widestStepOver returns the index of the largest step before i that rises past
// the value at i, or -1 when there is none.
func widestStepOver(values []float64, prev []int, i int) int {
	best, width := -1, 0.0
	for j := 0; j < i; j++ {
		if prev[j] < 0 || values[prev[j]] > values[i] || values[j] < values[i] {
			continue
		}
		if w := values[j] - values[prev[j]]; w > width {
			best, width = j, w
		}
	}
	return best
}

// cumulativeValues returns the values of points when all of them are running
// totals, and false when any is a delta, which has no interleaving to undo.
func cumulativeValues(points []InputPoint) ([]float64, bool) {
	values := make([]float64, len(points))
	for i, p := range points {
		if p.Delta {
			return nil, false
		}
		values[i] = p.Value
	}
	return values, true
}

// interleavedRates derives the rate of an interleaved series. The rate at a
// point is the growth of all chains over the trailing window of one reporting
// interval, so two pods reporting a second apart read as their combined rate
// with no spike from the small gap between them. A point that starts a chain has
// no rate of its own and yields no point.
func interleavedRates(points []InputPoint, values []float64) []DerivedPoint {
	prev := ChainPrevious(values)
	width := reportingInterval(points, prev)
	if width <= 0 {
		return []DerivedPoint{}
	}
	out := make([]DerivedPoint, 0, len(points))
	var inWindow float64
	start := 0
	for i, cur := range points {
		if prev[i] >= 0 {
			inWindow += values[i] - values[prev[i]]
		}
		for points[start].T <= cur.T-width {
			if prev[start] >= 0 {
				inWindow -= values[start] - values[prev[start]]
			}
			start++
		}
		if prev[i] >= 0 {
			out = append(out, DerivedPoint{T: cur.T, Value: inWindow / (float64(width) / 1e9), Count: cur.Count})
		}
	}
	return out
}

// reportingInterval is the median gap between consecutive points of a chain.
func reportingInterval(points []InputPoint, prev []int) int64 {
	gaps := make([]int64, 0, len(points))
	for i, p := range prev {
		if p >= 0 && points[i].T > points[p].T {
			gaps = append(gaps, points[i].T-points[p].T)
		}
	}
	if len(gaps) == 0 {
		return 0
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	return gaps[len(gaps)/2]
}

// sharedPoints reports whether any full label set among points is interleaved.
// Points grouped by a label key mix series that are each fine on their own, and
// those series need no change from their producer.
func sharedPoints(points []InputPoint) bool {
	bySeries := map[string][]float64{}
	for _, p := range points {
		if p.Delta {
			continue
		}
		key, _ := seriesKey(p.Attributes, nil)
		bySeries[key] = append(bySeries[key], p.Value)
	}
	for _, values := range bySeries {
		if Interleaved(values) {
			return true
		}
	}
	return false
}
