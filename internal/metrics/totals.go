package metrics

import "sort"

// Total is the summed increase of a counter over a range for one label group.
type Total struct {
	Labels map[string]string `json:"labels"`
	Total  float64           `json:"total"`
	// SharedSeries marks a group holding an interleaved series; see Series.
	SharedSeries bool `json:"shared_series,omitempty"`
}

// Totals sums how much each counter grew over its points, grouped by the
// groupBy label keys (all labels when empty). Increases are worked out per full
// series first, so two sessions that share a host are not differenced against
// each other, and only then added into their group. A delta point adds its
// value; a cumulative point adds the step up from the previous point, or its
// whole value after a reset. The first cumulative point of a series adds
// nothing, since its starting value is unknown. Only sums have a total; other
// types return nil. Groups come back largest first.
func Totals(metricType string, in []InputPoint, groupBy []string) []Total {
	if metricType != "sum" {
		return nil
	}
	type group struct {
		labels map[string]string
		total  float64
		shared bool
	}
	groups := map[string]*group{}
	var order []string

	bySeries := map[string][]InputPoint{}
	var seriesOrder []string
	for _, p := range in {
		key, _ := seriesKey(p.Attributes, nil)
		if _, ok := bySeries[key]; !ok {
			seriesOrder = append(seriesOrder, key)
		}
		bySeries[key] = append(bySeries[key], p)
	}
	for _, key := range seriesOrder {
		pts := bySeries[key]
		gkey, labels := seriesKey(pts[0].Attributes, groupBy)
		g := groups[gkey]
		if g == nil {
			g = &group{labels: labels}
			groups[gkey] = g
			order = append(order, gkey)
		}
		g.total += increase(pts)
		g.shared = g.shared || sharedPoints(pts)
	}

	out := make([]Total, 0, len(order))
	for _, k := range order {
		out = append(out, Total{Labels: groups[k].labels, Total: groups[k].total, SharedSeries: groups[k].shared})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}

// increase is how much one series grew across its points. An interleaved series
// adds the steps of its chains.
func increase(pts []InputPoint) float64 {
	var sum float64
	if values, ok := cumulativeValues(pts); ok && Interleaved(values) {
		for i, p := range ChainPrevious(values) {
			if p >= 0 {
				sum += values[i] - values[p]
			}
		}
		return sum
	}
	for i, p := range pts {
		switch {
		case p.Delta:
			sum += p.Value
		case i == 0:
		case p.Value >= pts[i-1].Value:
			sum += p.Value - pts[i-1].Value
		default: // counter reset
			sum += p.Value
		}
	}
	return sum
}
