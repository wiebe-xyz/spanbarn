package metrics

import (
	"encoding/json"
	"math"
	"testing"
)

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestRenderFor(t *testing.T) {
	cases := map[string]RenderKind{
		"gauge":         RenderLine,
		"sum":           RenderRate,
		"histogram":     RenderPercentile,
		"exp_histogram": RenderPercentile,
		"summary":       RenderPercentile,
		"weird":         RenderLine,
	}
	for typ, want := range cases {
		if got := RenderFor(typ); got != want {
			t.Errorf("RenderFor(%q) = %q, want %q", typ, got, want)
		}
	}
}

func TestDeriveGauge(t *testing.T) {
	pts := []InputPoint{{T: 1, Value: 42.5}, {T: 2, Value: 55}}
	got := Derive("gauge", pts)
	if len(got) != 2 || got[0].Value != 42.5 || got[1].Value != 55 {
		t.Fatalf("gauge derive wrong: %+v", got)
	}
}

func TestDeriveRate(t *testing.T) {
	// Cumulative counter increasing by 100 over 10s -> 10/sec.
	pts := []InputPoint{
		{T: 0, Value: 0},
		{T: 10e9, Value: 100},
		{T: 20e9, Value: 250}, // +150 over 10s -> 15/sec
	}
	got := Derive("sum", pts)
	if len(got) != 2 {
		t.Fatalf("want 2 rate points (n-1), got %d", len(got))
	}
	if !approx(got[0].Value, 10, 1e-9) {
		t.Errorf("first rate = %v, want 10", got[0].Value)
	}
	if !approx(got[1].Value, 15, 1e-9) {
		t.Errorf("second rate = %v, want 15", got[1].Value)
	}
}

func TestDeriveRateCounterReset(t *testing.T) {
	// Counter resets (300 -> 50): treat 50 as the increase over the interval.
	pts := []InputPoint{
		{T: 0, Value: 300},
		{T: 10e9, Value: 50},
	}
	got := Derive("sum", pts)
	if len(got) != 1 {
		t.Fatalf("want 1 point, got %d", len(got))
	}
	if !approx(got[0].Value, 5, 1e-9) { // 50 / 10s
		t.Errorf("reset rate = %v, want 5", got[0].Value)
	}
}

func TestDeriveRateZeroInterval(t *testing.T) {
	// Duplicate timestamp must not divide by zero; point is skipped.
	pts := []InputPoint{{T: 5e9, Value: 1}, {T: 5e9, Value: 2}}
	if got := Derive("sum", pts); len(got) != 0 {
		t.Errorf("want 0 points for zero interval, got %d", len(got))
	}
}

func TestHistogramQuantile(t *testing.T) {
	// Buckets: <=10 (5 obs), <=20 (5), <=50 (5), +Inf (0). Total 15.
	bounds := []float64{10, 20, 50}
	counts := []float64{5, 5, 5, 0}

	// p50 -> target 7.5, lands in 2nd bucket (10,20], cumBefore=5, frac=2.5/5=0.5 -> 15.
	if v := histogramQuantile(bounds, counts, 0.50); !approx(v, 15, 1e-9) {
		t.Errorf("p50 = %v, want 15", v)
	}
	// p95 -> target 14.25, 3rd bucket (20,50], cumBefore=10, frac=4.25/5=0.85 -> 20+0.85*30=45.5
	if v := histogramQuantile(bounds, counts, 0.95); !approx(v, 45.5, 1e-9) {
		t.Errorf("p95 = %v, want 45.5", v)
	}
}

func TestHistogramQuantileFirstBucket(t *testing.T) {
	// First bucket starts at 0: <=100 holds everything.
	bounds := []float64{100}
	counts := []float64{10, 0}
	// p50 target 5 -> bucket 0 [0,100], frac=5/10=0.5 -> 50.
	if v := histogramQuantile(bounds, counts, 0.50); !approx(v, 50, 1e-9) {
		t.Errorf("p50 = %v, want 50", v)
	}
}

func TestHistogramQuantileEmpty(t *testing.T) {
	if v := histogramQuantile(nil, nil, 0.5); v != 0 {
		t.Errorf("empty histogram quantile = %v, want 0", v)
	}
	if v := histogramQuantile([]float64{10}, []float64{0, 0}, 0.5); v != 0 {
		t.Errorf("all-zero histogram quantile = %v, want 0", v)
	}
}

func TestDeriveHistogramFromExtra(t *testing.T) {
	extra := json.RawMessage(`{"bounds":[10,20,50],"counts":[5,5,5,0]}`)
	got := Derive("histogram", []InputPoint{{T: 1, Extra: extra, Count: 15}})
	if len(got) != 1 || got[0].P50 == nil || got[0].P95 == nil {
		t.Fatalf("histogram derive missing percentiles: %+v", got)
	}
	if !approx(*got[0].P50, 15, 1e-9) {
		t.Errorf("p50 = %v, want 15", *got[0].P50)
	}
}

func TestExpHistogramPercentiles(t *testing.T) {
	// scale 0 -> base 2. Positive buckets at offset 0: index0 (1,2], index1 (2,4], index2 (4,8].
	// 10 obs in each. zero_count 0.
	extra := json.RawMessage(`{"scale":0,"zero_count":0,"positive":{"offset":0,"bucket_counts":[10,10,10]}}`)
	p50, p95, p99 := expHistogramPercentiles(extra)
	// p50 -> target 15 of 30, lands in 2nd bucket upper bound 4, lower 2 -> within (2,4].
	if p50 < 2 || p50 > 4 {
		t.Errorf("p50 = %v, want in (2,4]", p50)
	}
	// p95/p99 land in the top bucket (4,8].
	if p95 < 4 || p95 > 8 {
		t.Errorf("p95 = %v, want in (4,8]", p95)
	}
	if p99 < 4 || p99 > 8 {
		t.Errorf("p99 = %v, want in (4,8]", p99)
	}
}

func TestSummaryPercentiles(t *testing.T) {
	extra := json.RawMessage(`{"quantiles":[{"quantile":0.5,"value":12},{"quantile":0.95,"value":88},{"quantile":0.99,"value":120}]}`)
	got := Derive("summary", []InputPoint{{T: 1, Extra: extra}})
	if len(got) != 1 {
		t.Fatalf("want 1 point")
	}
	if *got[0].P50 != 12 || *got[0].P95 != 88 || *got[0].P99 != 120 {
		t.Errorf("summary quantiles wrong: %v %v %v", *got[0].P50, *got[0].P95, *got[0].P99)
	}
}

func TestSummaryNearestQuantile(t *testing.T) {
	// Only 0.5 and 0.9 stored; p95 should snap to nearest (0.9 -> 80), p99 too.
	extra := json.RawMessage(`{"quantiles":[{"quantile":0.5,"value":10},{"quantile":0.9,"value":80}]}`)
	got := Derive("summary", []InputPoint{{T: 1, Extra: extra}})
	if *got[0].P95 != 80 || *got[0].P99 != 80 {
		t.Errorf("nearest-quantile snap wrong: p95=%v p99=%v", *got[0].P95, *got[0].P99)
	}
}

func TestBuildSeriesSplitByAttributes(t *testing.T) {
	// Two gauges with different service labels -> two series.
	in := []InputPoint{
		{T: 1, Value: 1, Attributes: map[string]string{"service.name": "web"}},
		{T: 2, Value: 2, Attributes: map[string]string{"service.name": "api"}},
		{T: 3, Value: 3, Attributes: map[string]string{"service.name": "web"}},
	}
	got := BuildSeries("gauge", in, nil)
	if len(got) != 2 {
		t.Fatalf("want 2 series, got %d", len(got))
	}
	// First-seen order preserved: web first.
	if got[0].Labels["service.name"] != "web" || len(got[0].Points) != 2 {
		t.Errorf("web series wrong: %+v", got[0])
	}
	if got[1].Labels["service.name"] != "api" || len(got[1].Points) != 1 {
		t.Errorf("api series wrong: %+v", got[1])
	}
}

func TestBuildSeriesGroupBy(t *testing.T) {
	// Group by route only; service is ignored so both points merge into one series.
	in := []InputPoint{
		{T: 1, Value: 1, Attributes: map[string]string{"route": "/a", "service.name": "web"}},
		{T: 2, Value: 2, Attributes: map[string]string{"route": "/a", "service.name": "api"}},
		{T: 3, Value: 3, Attributes: map[string]string{"route": "/b", "service.name": "web"}},
	}
	got := BuildSeries("gauge", in, []string{"route"})
	if len(got) != 2 {
		t.Fatalf("want 2 series (one per route), got %d", len(got))
	}
	for _, s := range got {
		if _, ok := s.Labels["service.name"]; ok {
			t.Errorf("group-by should drop non-grouped labels, got %+v", s.Labels)
		}
	}
}
