package aggregation

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// opaqueRecord is one cumulative export of a distribution whose buckets the
// accumulator keeps as an opaque snapshot.
func opaqueRecord(typ model.MetricType, tNano uint64, sum float64, obs uint64) model.MetricRecord {
	return model.MetricRecord{
		ProjectID: 1, Name: "dur", Type: typ, TimeUnixNano: tNano, Value: sum, Count: obs,
		Temporality: "cumulative", Extra: []byte(`{}`),
	}
}

// TestMetricAccumulatorOpaqueSnapshotCounts: three cumulative exports landing in
// one bucket report a running total three times. The bucket holds the newest one.
func TestMetricAccumulatorOpaqueSnapshotCounts(t *testing.T) {
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	for _, typ := range []model.MetricType{model.MetricTypeExponentialHistogram, model.MetricTypeSummary} {
		w := &fakeRollupWriter{}
		acc := NewMetricAccumulator(w, 5*time.Minute, time.Minute, nil)
		now := base.Add(10 * time.Minute)
		acc.now = func() time.Time { return now }

		for i, obs := range []uint64{100, 110, 120} {
			at := uint64(base.Add(time.Duration(i+1) * 10 * time.Second).UnixNano())
			acc.AddMetric(opaqueRecord(typ, at, float64(obs)*2, obs))
		}
		if err := acc.Flush(context.Background()); err != nil {
			t.Fatalf("%s flush: %v", typ, err)
		}
		if len(w.got) != 1 {
			t.Fatalf("%s rollups = %d, want 1", typ, len(w.got))
		}
		if got := w.got[0].ObsCount; got != 120 {
			t.Errorf("%s obs_count = %d, want 120", typ, got)
		}
		if got := w.got[0].Sum; got != 240 {
			t.Errorf("%s sum = %v, want 240", typ, got)
		}
	}
}
