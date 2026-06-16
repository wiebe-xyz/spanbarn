package metrics

import "testing"

// gaugePoints builds evenly-spaced gauge input points starting at t0 (nanos),
// one per minute.
func gaugePoints(t0 int64, vals ...float64) []InputPoint {
	const minute = int64(60_000_000_000)
	pts := make([]InputPoint, len(vals))
	for i, v := range vals {
		pts[i] = InputPoint{T: t0 + int64(i)*minute, Value: v}
	}
	return pts
}

func TestDetectSeriesSpike(t *testing.T) {
	pts := gaugePoints(0, 10, 10, 10, 10, 50, 52) // baseline ~10, recent ~51
	split := pts[4].T
	in, ok := DetectSeries("cpu.load", "gauge", nil, pts, split)
	if !ok {
		t.Fatal("expected a spike insight")
	}
	if in.Kind != "spike" {
		t.Errorf("kind = %q, want spike", in.Kind)
	}
	if in.ChangePct < 0.5 {
		t.Errorf("changePct = %v, want >= 0.5", in.ChangePct)
	}
}

func TestDetectSeriesDrop(t *testing.T) {
	pts := gaugePoints(0, 100, 100, 100, 100, 20, 18)
	in, ok := DetectSeries("cpu.load", "gauge", nil, pts, pts[4].T)
	if !ok || in.Kind != "drop" {
		t.Fatalf("expected drop, got %+v ok=%v", in, ok)
	}
	if in.ChangePct >= 0 {
		t.Errorf("drop changePct should be negative, got %v", in.ChangePct)
	}
}

func TestDetectSeriesStableNoInsight(t *testing.T) {
	pts := gaugePoints(0, 50, 51, 49, 50, 50, 51)
	if _, ok := DetectSeries("cpu.load", "gauge", nil, pts, pts[4].T); ok {
		t.Error("stable series should produce no insight")
	}
}

func TestDetectSeriesNew(t *testing.T) {
	// All points are in the recent window (no baseline).
	pts := gaugePoints(1000, 5, 6, 7)
	in, ok := DetectSeries("new.metric", "gauge", nil, pts, 0)
	if !ok || in.Kind != "new_series" {
		t.Fatalf("expected new_series, got %+v ok=%v", in, ok)
	}
	if in.Magnitude() <= 1e9 {
		t.Error("new_series should rank above ordinary changes")
	}
}

func TestDetectSeriesHistogramRegression(t *testing.T) {
	const minute = int64(60_000_000_000)
	mk := func(i int, p95 float64) InputPoint {
		// Single-bucket histogram whose only bucket upper bound is p95, so the
		// reconstructed p95 tracks that bound.
		return InputPoint{T: int64(i) * minute, Count: 10,
			Extra: []byte(`{"bounds":[` + ftoa(p95) + `],"counts":[10,0]}`)}
	}
	pts := []InputPoint{mk(0, 100), mk(1, 100), mk(2, 100), mk(3, 100), mk(4, 400), mk(5, 400)}
	in, ok := DetectSeries("http.server.duration", "histogram", nil, pts, pts[4].T)
	if !ok {
		t.Fatal("expected a regression insight")
	}
	if in.Kind != "regression" {
		t.Errorf("kind = %q, want regression", in.Kind)
	}
}

// ftoa is a tiny float formatter to keep the test JSON literals readable.
func ftoa(f float64) string {
	switch f {
	case 100:
		return "100"
	case 400:
		return "400"
	default:
		return "0"
	}
}
