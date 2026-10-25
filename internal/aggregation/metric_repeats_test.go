package aggregation

import (
	"slices"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const minute = uint64(time.Minute)

func cumulative(start uint64, minuteN int, value float64) model.MetricRecord {
	return model.MetricRecord{
		ProjectID: 1, Name: "http.server.request.duration", Type: model.MetricTypeHistogram,
		Temporality: "cumulative", StartTimeUnixNano: start, TimeUnixNano: start + uint64(minuteN)*minute,
		Value: value, Count: uint64(value), Attributes: []byte(`{"http.route":"/"}`),
		Extra: []byte(`{"bounds":[1],"counts":[0,1]}`),
	}
}

func minutesOf(recs []model.MetricRecord, start uint64) []int {
	out := make([]int, len(recs))
	for i, r := range recs {
		out[i] = int((r.TimeUnixNano - start) / minute)
	}
	return out
}

// Repeats are held, and the last one is written just before the stream
// changes, so the increase is measured over the minute it happened in.
func TestMetricRepeatFilterWritesTheHeldPointBeforeAChange(t *testing.T) {
	f := NewMetricRepeatFilter(5 * time.Minute)
	const start = uint64(1_000_000) * minute
	var kept []model.MetricRecord
	for i, v := range []float64{10, 10, 10, 10, 12, 12} {
		kept = append(kept, f.Keep([]model.MetricRecord{cumulative(start, i, v)})...)
	}
	if got, want := minutesOf(kept, start), []int{0, 3, 4}; !slices.Equal(got, want) {
		t.Fatalf("kept minutes %v, want %v", got, want)
	}
}

// An unchanged stream writes one point per heartbeat.
func TestMetricRepeatFilterWritesAHeartbeat(t *testing.T) {
	f := NewMetricRepeatFilter(5 * time.Minute)
	const start = uint64(1_000_000) * minute
	var batch []model.MetricRecord
	for i := range 12 {
		batch = append(batch, cumulative(start, i, 7))
	}
	if got, want := minutesOf(f.Keep(batch), start), []int{0, 5, 10}; !slices.Equal(got, want) {
		t.Fatalf("kept minutes %v, want %v", got, want)
	}
}

// Two pods export the same attribute set with their own start times. Each is
// its own stream, so their alternating totals are repeats of themselves.
func TestMetricRepeatFilterKeepsPodsApart(t *testing.T) {
	f := NewMetricRepeatFilter(5 * time.Minute)
	podA, podB := uint64(1_000_000)*minute, uint64(1_000_000)*minute+uint64(time.Second)
	var batch []model.MetricRecord
	for i := range 4 {
		batch = append(batch, cumulative(podA, i, 13), cumulative(podB, i, 7))
	}
	kept := f.Keep(batch)
	if len(kept) != 2 || kept[0].Value != 13 || kept[1].Value != 7 {
		t.Fatalf("kept %d points %v, want one per pod", len(kept), kept)
	}
}

// Gauges, delta points and points without a start time are all written.
func TestMetricRepeatFilterPassesOtherPoints(t *testing.T) {
	f := NewMetricRepeatFilter(5 * time.Minute)
	const start = uint64(1_000_000) * minute
	var batch []model.MetricRecord
	for i := range 3 {
		g := cumulative(start, i, 1)
		g.Type, g.Temporality = model.MetricTypeGauge, ""
		d := cumulative(start, i, 1)
		d.Temporality = "delta"
		n := cumulative(start, i, 1)
		n.StartTimeUnixNano = 0
		batch = append(batch, g, d, n)
	}
	if kept := f.Keep(batch); len(kept) != len(batch) {
		t.Fatalf("kept %d of %d points", len(kept), len(batch))
	}
}

// A zero heartbeat disables the filter.
func TestMetricRepeatFilterDisabled(t *testing.T) {
	f := NewMetricRepeatFilter(0)
	const start = uint64(1_000_000) * minute
	batch := []model.MetricRecord{cumulative(start, 0, 1), cumulative(start, 1, 1)}
	if kept := f.Keep(batch); len(kept) != 2 {
		t.Fatalf("kept %d of 2 points with the filter off", len(kept))
	}
}

// A stream not seen for three heartbeats is forgotten, and its next point is
// written as the first of a new stream.
func TestMetricRepeatFilterForgetsIdleStreams(t *testing.T) {
	f := NewMetricRepeatFilter(5 * time.Minute)
	wall := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)
	f.now = func() time.Time { return wall }
	const start = uint64(1_000_000) * minute
	f.Keep([]model.MetricRecord{cumulative(start, 0, 1)})
	wall = wall.Add(15 * time.Minute)
	f.Keep(nil)
	if len(f.streams) != 0 {
		t.Fatalf("%d streams after 15 idle minutes, want 0", len(f.streams))
	}
	if kept := f.Keep([]model.MetricRecord{cumulative(start, 16, 1)}); len(kept) != 1 {
		t.Fatalf("kept %d points of a forgotten stream, want 1", len(kept))
	}
}
