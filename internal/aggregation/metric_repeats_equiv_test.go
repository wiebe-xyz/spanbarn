package aggregation

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// equivStream is 90 minutes of one series exported once a minute: changes at
// a few minutes, a counter reset at minute 25 with the same start time, and a
// process restart (a new start time, counting from zero) at minute 40.
func equivStream(typ model.MetricType) []model.MetricRecord {
	startA := uint64(1_000_000) * minute
	startB := startA + 40*minute - uint64(time.Second)
	bounds := []float64{0.01, 0.1, 1}
	counts := []float64{0, 0, 0, 0}
	var total float64
	var out []model.MetricRecord
	for m := range 90 {
		start := startA
		switch {
		case m == 25:
			total, counts = 0, []float64{0, 0, 0, 0}
		case m == 40:
			total, counts = 0, []float64{0, 0, 0, 0}
		}
		if m >= 40 {
			start = startB
		}
		switch m {
		case 7, 8, 20, 25, 33, 41, 47, 70, 71, 88:
			total += float64(m%7 + 1)
			counts[m%4] += float64(m%3 + 1)
		}
		rec := model.MetricRecord{
			ProjectID: 1, Name: "x", Type: typ, Temporality: "cumulative",
			StartTimeUnixNano: start, TimeUnixNano: startA + uint64(m)*minute,
			Value: total, Attributes: []byte(`{"route":"/"}`),
		}
		if typ == model.MetricTypeHistogram {
			var n float64
			for _, c := range counts {
				n += c
			}
			rec.Count = uint64(n)
			rec.Extra, _ = json.Marshal(map[string]any{"bounds": bounds, "counts": slices.Clone(counts)})
		}
		out = append(out, rec)
	}
	return out
}

// filtered runs recs through a filter one export (one point) per batch, the
// way the consumer receives them.
func filtered(recs []model.MetricRecord, heartbeat time.Duration) []model.MetricRecord {
	f := NewMetricRepeatFilter(heartbeat)
	var out []model.MetricRecord
	for _, r := range recs {
		out = append(out, f.Keep([]model.MetricRecord{r})...)
	}
	return out
}

// window returns the points of recs in [from, to] minutes, sorted, as the raw
// query hands them to the derivation.
func window(recs []model.MetricRecord, from, to int) []metrics.InputPoint {
	t0 := recs[0].TimeUnixNano
	var in []metrics.InputPoint
	for _, r := range recs {
		m := int((r.TimeUnixNano - t0) / minute)
		if m >= from && m <= to {
			in = append(in, metrics.InputPoint{T: int64(r.TimeUnixNano), Value: r.Value, Count: int64(r.Count), Extra: r.Extra})
		}
	}
	slices.SortFunc(in, func(a, b metrics.InputPoint) int { return int(a.T - b.T) })
	return in
}

// increase integrates the derived per-second rate over the window.
func increase(in []metrics.InputPoint) float64 {
	var sum float64
	for i, d := range metrics.Derive("sum", in) {
		// Every point after the first has a rate; derived point i is input i+1.
		sum += d.Value * float64(d.T-in[i].T) / 1e9
	}
	return sum
}

// windowQuantiles are the p50/p95/p99 of the observations between the
// window's first and last histogram, or of the last one after a reset.
func windowQuantiles(t *testing.T, in []metrics.InputPoint) string {
	t.Helper()
	if len(in) == 0 {
		return "none"
	}
	type hist struct {
		Bounds []float64 `json:"bounds"`
		Counts []float64 `json:"counts"`
	}
	var first, last hist
	if err := json.Unmarshal(in[0].Extra, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(in[len(in)-1].Extra, &last); err != nil {
		t.Fatal(err)
	}
	diff := slices.Clone(last.Counts)
	for i := range diff {
		diff[i] -= first.Counts[i]
		if diff[i] < 0 {
			diff = slices.Clone(last.Counts)
			break
		}
	}
	extra, _ := json.Marshal(hist{last.Bounds, diff})
	d := metrics.Derive("histogram", []metrics.InputPoint{{T: in[0].T, Extra: extra}})
	return fmt.Sprintf("%.6g/%.6g/%.6g", *d[0].P50, *d[0].P95, *d[0].P99)
}

// Over every window of at least one heartbeat, the increase derived from the
// stored points and the histogram quantiles are the same with the filter on
// and off, across a counter reset and a process restart.
func TestMetricRepeatFilterKeepsWindowRatesAndQuantiles(t *testing.T) {
	const heartbeat = 5 * time.Minute
	sum, hist := equivStream(model.MetricTypeSum), equivStream(model.MetricTypeHistogram)
	sumOn, histOn := filtered(sum, heartbeat), filtered(hist, heartbeat)
	if len(sumOn) >= len(sum)/2 {
		t.Fatalf("filter kept %d of %d points", len(sumOn), len(sum))
	}
	for from := 0; from < 90; from++ {
		for to := from + 5; to < 90; to++ {
			off, on := increase(window(sum, from, to)), increase(window(sumOn, from, to))
			if math.Abs(off-on) > 1e-9 {
				t.Errorf("increase over [%d, %d]: %g filtered, %g unfiltered", from, to, on, off)
			}
			if off, on := windowQuantiles(t, window(hist, from, to)), windowQuantiles(t, window(histOn, from, to)); off != on {
				t.Errorf("quantiles over [%d, %d]: %s filtered, %s unfiltered", from, to, on, off)
			}
		}
	}
	t.Logf("stored %d of %d points", len(sumOn), len(sum))
}
