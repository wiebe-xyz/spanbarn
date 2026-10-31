package metrics

import (
	"math"
	"testing"
)

// twoPods alternates two pods on one label set, each reporting every minute a
// second apart. Pod a grows by 60 a minute from 1000, pod b by 120 from 10.
func twoPods(minutes int) []InputPoint {
	var pts []InputPoint
	for m := 0; m < minutes; m++ {
		t := int64(m) * 60e9
		pts = append(pts,
			InputPoint{T: t, Value: 1000 + float64(m)*60, Count: 1},
			InputPoint{T: t + 1e9, Value: 10 + float64(m)*120, Count: 1},
		)
	}
	return pts
}

func TestInterleavedDetection(t *testing.T) {
	cases := []struct {
		name   string
		values []float64
		want   bool
	}{
		{"steady rise", []float64{1, 2, 3, 4, 5}, false},
		{"one real reset", []float64{100, 200, 300, 5, 10, 20}, false},
		{"two real resets", []float64{100, 200, 5, 50, 90, 3, 40}, false},
		{"one restart that catches up", []float64{5, 1, 6, 7}, false},
		{"two pods", []float64{11, 1, 11, 1, 11}, true},
		{"three pods", []float64{100, 50, 10, 101, 51, 11, 102, 52, 12}, true},
		{"quiet pod among busy ones", []float64{900, 910, 920, 5, 930, 940, 950, 6, 960}, true},
		{"too short", []float64{11, 1}, false},
	}
	for _, c := range cases {
		if got := Interleaved(c.values); got != c.want {
			t.Errorf("%s: Interleaved(%v) = %v, want %v", c.name, c.values, got, c.want)
		}
	}
}

func TestChainPreviousSeparatesPods(t *testing.T) {
	got := ChainPrevious([]float64{1000, 10, 1060, 130, 1120, 250})
	want := []int{-1, -1, 0, 1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ChainPrevious = %v, want %v", got, want)
		}
	}
}

// A crossed pairing moves the same total: pods near each other swap chains and
// the growth still adds up.
func TestChainStepsKeepTheTotalWhenPairingCrosses(t *testing.T) {
	values := []float64{100, 105, 103, 106, 110, 107, 112, 108}
	prev := ChainPrevious(values)
	var sum float64
	for i, p := range prev {
		if p >= 0 {
			sum += values[i] - values[p]
		}
	}
	// Pod a: 100, 103, 110, 112. Pod b: 105, 106, 107, 108.
	if want := float64(12 + 3); sum != want {
		t.Fatalf("sum of chain steps = %v, want %v", sum, want)
	}
}

// The low pod reporting first must not leave the gap to the high pod as growth,
// however many pods there are.
func TestChainStepsDropTheGapBetweenPods(t *testing.T) {
	cases := [][]float64{
		{40, 8000, 40, 8000, 40, 8000},
		{8000, 40, 8000, 40, 8000, 40},
		{10, 500, 8000, 10, 500, 8000, 10, 500, 8000},
		{500, 10, 8000, 500, 10, 8000, 500, 10, 8000},
	}
	for _, values := range cases {
		prev := ChainPrevious(values)
		for i, p := range prev {
			if p >= 0 && values[i] != values[p] {
				t.Fatalf("%v: step %v to %v, chains %v", values, values[p], values[i], prev)
			}
		}
	}
}

func TestDeriveRateReadsSharedSeriesAsCombinedRate(t *testing.T) {
	pts := Derive("sum", twoPods(6))
	if len(pts) == 0 {
		t.Fatal("no points")
	}
	// 60 and 120 a minute together is 3 per second, and the first full window
	// onward reads that with no spike from the one second between the pods.
	for _, p := range pts[1:] {
		if math.Abs(p.Value-3) > 1e-9 {
			t.Fatalf("rate at %d = %v, want 3 (all: %+v)", p.T, p.Value, pts)
		}
	}
}

// Flat counters whose alternation alone sums to a large rate. Nothing grew, so
// nothing may be read.
func TestDeriveRateFlatSharedSeriesReadsZero(t *testing.T) {
	var in []InputPoint
	for i := 0; i < 11; i++ {
		v := 8000.0
		if i%2 == 1 {
			v = 40
		}
		in = append(in, InputPoint{T: int64(i) * 30e9, Value: v, Count: 1})
	}
	for _, p := range Derive("sum", in) {
		if p.Value != 0 {
			t.Fatalf("rate at %d = %v, want 0", p.T, p.Value)
		}
	}
	if total := increase(in); total != 0 {
		t.Fatalf("increase = %v, want 0", total)
	}
}

func TestDeriveRateSingleProducerKeepsReset(t *testing.T) {
	in := []InputPoint{
		{T: 0, Value: 100},
		{T: 60e9, Value: 160},
		{T: 120e9, Value: 30}, // restart: the value itself is the increase
		{T: 180e9, Value: 90},
	}
	got := Derive("sum", in)
	want := []float64{1, 0.5, 1}
	if len(got) != len(want) {
		t.Fatalf("points = %+v, want %v", got, want)
	}
	for i := range want {
		if got[i].Value != want[i] {
			t.Fatalf("rate %d = %v, want %v", i, got[i].Value, want[i])
		}
	}
	if total := increase(in); total != 60+30+60 {
		t.Fatalf("increase = %v, want 150", total)
	}
}

func TestDeriveRateDeltaIgnoresInterleaving(t *testing.T) {
	in := []InputPoint{
		{T: 60e9, Value: 11, Delta: true, IntervalNano: 60e9},
		{T: 120e9, Value: 1, Delta: true, IntervalNano: 60e9},
		{T: 180e9, Value: 11, Delta: true, IntervalNano: 60e9},
		{T: 240e9, Value: 1, Delta: true, IntervalNano: 60e9},
		{T: 300e9, Value: 11, Delta: true, IntervalNano: 60e9},
	}
	got := Derive("sum", in)
	if len(got) != 5 || got[0].Value != 11.0/60 || got[1].Value != 1.0/60 {
		t.Fatalf("delta points = %+v", got)
	}
	if s := BuildSeries("sum", in, nil); s[0].SharedSeries {
		t.Fatal("delta series flagged as shared")
	}
}

func TestTotalsSharedSeries(t *testing.T) {
	got := Totals("sum", twoPods(6), nil)
	// Five minutes of growth: 5*60 on pod a and 5*120 on pod b.
	if len(got) != 1 || got[0].Total != 900 || !got[0].SharedSeries {
		t.Fatalf("totals = %+v, want 900 flagged", got)
	}
}

func TestBuildSeriesFlagsOnlySharedLabelSets(t *testing.T) {
	var in []InputPoint
	for _, p := range twoPods(6) {
		p.Attributes = map[string]string{"route": "/shared"}
		in = append(in, p)
	}
	for m := 0; m < 6; m++ {
		in = append(in, InputPoint{T: int64(m) * 60e9, Value: float64(m) * 60, Attributes: map[string]string{"route": "/alone"}})
	}
	for _, s := range BuildSeries("sum", in, nil) {
		if want := s.Labels["route"] == "/shared"; s.SharedSeries != want {
			t.Fatalf("series %v shared = %v, want %v", s.Labels, s.SharedSeries, want)
		}
	}
}

// Pods with their own instance id are fine on their own, so grouping them away
// asks nothing of the producer, while the group still reads the combined rate.
func TestBuildSeriesGroupOfGoodSeriesIsNotFlagged(t *testing.T) {
	var in []InputPoint
	for i, p := range twoPods(6) {
		id := "a"
		if i%2 == 1 {
			id = "b"
		}
		p.Attributes = map[string]string{"route": "/x", "service.instance.id": id}
		in = append(in, p)
	}
	got := BuildSeries("sum", in, []string{"route"})
	if len(got) != 1 || got[0].SharedSeries {
		t.Fatalf("series = %+v, want one unflagged", got)
	}
	for _, p := range got[0].Points[1:] {
		if math.Abs(p.Value-3) > 1e-9 {
			t.Fatalf("grouped rate = %v, want 3", p.Value)
		}
	}
}
