package metrics

import "math"

// Insight describes a notable change in a single metric series: a spike, a drop,
// a percentile regression, or a newly-appeared series. It lets the UI surface
// what changed instead of making the user eyeball every graph.
type Insight struct {
	Metric    string            `json:"metric"`
	Labels    map[string]string `json:"labels"`
	Kind      string            `json:"kind"`   // spike | drop | regression | new_series
	Render    string            `json:"render"` // how to chart the metric (see RenderFor)
	Baseline  float64           `json:"baseline"`
	Recent    float64           `json:"recent"`
	ChangePct float64           `json:"changePct"` // signed fractional change (0.5 = +50%); 0 for new_series
}

// Detection thresholds.
const (
	insightMinBaseline = 3   // baseline buckets required to judge a change
	insightMinNew      = 2   // recent buckets required to call a series "new"
	insightThreshold   = 0.5 // minimum |change| (50%) to report a spike/drop
	insightCappedPct   = 10  // sentinel change when the baseline mean is ~0
	insightEpsilon     = 1e-9
)

// DetectSeries compares a series' recent window (buckets at or after splitNano)
// to its baseline (buckets before splitNano) and returns an Insight when the
// change is notable. points must be sorted ascending by time.
func DetectSeries(name, metricType string, labels map[string]string, points []InputPoint, splitNano int64) (Insight, bool) {
	render := RenderFor(metricType)
	derived := Derive(metricType, points)

	var base, recent []float64
	for _, d := range derived {
		v := scalarOf(render, d)
		if d.T < splitNano {
			base = append(base, v)
		} else {
			recent = append(recent, v)
		}
	}
	if len(recent) == 0 {
		return Insight{}, false
	}
	recentMean := mean(recent)

	if len(base) < insightMinBaseline {
		// No real baseline: report as a newly-appeared series if it has enough
		// recent activity, otherwise stay quiet.
		if len(base) == 0 && len(recent) >= insightMinNew {
			return Insight{Metric: name, Labels: labels, Kind: "new_series", Render: string(render), Recent: recentMean}, true
		}
		return Insight{}, false
	}

	baseMean := mean(base)
	var pct float64
	if math.Abs(baseMean) < insightEpsilon {
		if math.Abs(recentMean) < insightEpsilon {
			return Insight{}, false
		}
		pct = insightCappedPct
		if recentMean < 0 {
			pct = -insightCappedPct
		}
	} else {
		pct = (recentMean - baseMean) / math.Abs(baseMean)
	}

	if math.Abs(pct) < insightThreshold {
		return Insight{}, false
	}

	kind := "spike"
	if pct < 0 {
		kind = "drop"
	} else if render == RenderPercentile {
		// A rising distribution is a latency/size regression.
		kind = "regression"
	}

	return Insight{
		Metric:    name,
		Labels:    labels,
		Kind:      kind,
		Render:    string(render),
		Baseline:  baseMean,
		Recent:    recentMean,
		ChangePct: pct,
	}, true
}

// Magnitude ranks insights: new series first, then by the size of the change.
func (i Insight) Magnitude() float64 {
	if i.Kind == "new_series" {
		return math.MaxFloat64
	}
	return math.Abs(i.ChangePct)
}

// scalarOf reduces a derived point to the single value used for comparison:
// p95 for distributions, the value/rate otherwise.
func scalarOf(render RenderKind, d DerivedPoint) float64 {
	if render == RenderPercentile {
		if d.P95 != nil {
			return *d.P95
		}
		return 0
	}
	return d.Value
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
