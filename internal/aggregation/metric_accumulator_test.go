package aggregation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type fakeRollupWriter struct{ got []repository.MetricRollup }

func (f *fakeRollupWriter) UpsertMetricRollups(r []repository.MetricRollup) error {
	f.got = append(f.got, r...)
	return nil
}

func gaugeRec(name string, attrs string, tNano uint64, v float64) model.MetricRecord {
	return model.MetricRecord{
		ProjectID: 1, Name: name, Type: model.MetricTypeGauge,
		TimeUnixNano: tNano, Value: v, Attributes: json.RawMessage(attrs),
	}
}

func TestMetricAccumulatorGaugeFold(t *testing.T) {
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	w := &fakeRollupWriter{}
	acc := NewMetricAccumulator(w, time.Minute, time.Minute, nil)
	now := base.Add(90 * time.Second)
	acc.now = func() time.Time { return now }

	acc.AddMetric(gaugeRec("g", `{"a":"1"}`, uint64(base.Add(10*time.Second).UnixNano()), 10))
	acc.AddMetric(gaugeRec("g", `{"a":"1"}`, uint64(base.Add(20*time.Second).UnixNano()), 30))
	acc.AddMetric(gaugeRec("g", `{"a":"1"}`, uint64(base.Add(30*time.Second).UnixNano()), 20))

	if err := acc.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(w.got) != 1 {
		t.Fatalf("want 1 rollup, got %d", len(w.got))
	}
	r := w.got[0]
	if r.Count != 3 || r.Sum != 60 || r.Min != 10 || r.Max != 30 || r.Last != 20 {
		t.Errorf("gauge fold wrong: %+v", r)
	}
	if r.Bucket.UTC() != base {
		t.Errorf("bucket = %v, want %v", r.Bucket.UTC(), base)
	}
}

func TestMetricAccumulatorKeepsOpenBucket(t *testing.T) {
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	w := &fakeRollupWriter{}
	acc := NewMetricAccumulator(w, time.Minute, time.Minute, nil)
	now := base.Add(90 * time.Second)
	acc.now = func() time.Time { return now }

	// Closed bucket (base..base+1m) and an open bucket (base+1m..base+2m).
	acc.AddMetric(gaugeRec("g", `{}`, uint64(base.Add(10*time.Second).UnixNano()), 5))
	acc.AddMetric(gaugeRec("g", `{}`, uint64(base.Add(75*time.Second).UnixNano()), 9))

	if err := acc.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(w.got) != 1 {
		t.Fatalf("want only the closed bucket flushed, got %d", len(w.got))
	}
	if w.got[0].Bucket.UTC() != base {
		t.Errorf("flushed wrong bucket %v", w.got[0].Bucket.UTC())
	}

	// The open bucket is still buffered; advancing time closes it.
	now = base.Add(150 * time.Second)
	if err := acc.Flush(context.Background()); err != nil {
		t.Fatalf("flush 2: %v", err)
	}
	if len(w.got) != 2 {
		t.Fatalf("want 2 rollups after time advances, got %d", len(w.got))
	}
	if w.got[1].Last != 9 {
		t.Errorf("open-bucket value wrong: %+v", w.got[1])
	}
}

// histogramRecord builds one explicit-bucket histogram point.
func histogramRecord(temporality string, tNano uint64, counts string, obs uint64, sum float64) model.MetricRecord {
	return model.MetricRecord{
		ProjectID: 1, Name: "h", Type: model.MetricTypeHistogram,
		TimeUnixNano: tNano, Value: sum, Count: obs,
		Temporality: temporality,
		Attributes:  json.RawMessage(`{}`),
		Extra:       json.RawMessage(`{"bounds":[10,20],"counts":` + counts + `}`),
	}
}

func flushOne(t *testing.T, acc *MetricAccumulator, w *fakeRollupWriter) repository.MetricRollup {
	t.Helper()
	if err := acc.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(w.got) != 1 {
		t.Fatalf("want 1 rollup, got %d", len(w.got))
	}
	return w.got[0]
}

func histogramCounts(t *testing.T, extra string) []float64 {
	t.Helper()
	var h struct {
		Bounds []float64 `json:"bounds"`
		Counts []float64 `json:"counts"`
	}
	if err := json.Unmarshal([]byte(extra), &h); err != nil {
		t.Fatalf("unmarshal extra: %v", err)
	}
	return h.Counts
}

// TestMetricAccumulatorCumulativeHistogramKeepsNewestSnapshot: a cumulative
// point carries totals since process start, so the newest one describes the
// bucket. Adding them multiplied the distribution by the number of exports that
// happened to land in the window — at a 10s export interval a 5-minute bucket
// claimed 30 times the observations it saw. It is also what lets compaction
// difference consecutive buckets into real per-window distributions.
//
// A point with no temporality is treated as cumulative, which is the SDK default.
func TestMetricAccumulatorCumulativeHistogramKeepsNewestSnapshot(t *testing.T) {
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	w := &fakeRollupWriter{}
	acc := NewMetricAccumulator(w, time.Minute, time.Minute, nil)
	acc.now = func() time.Time { return base.Add(90 * time.Second) }

	acc.AddMetric(histogramRecord("", uint64(base.Add(5*time.Second).UnixNano()), `[1,2,0]`, 3, 30))
	acc.AddMetric(histogramRecord("cumulative", uint64(base.Add(15*time.Second).UnixNano()), `[3,1,1]`, 5, 55))

	r := flushOne(t, acc, w)
	if r.ObsCount != 5 || r.Sum != 55 {
		t.Errorf("totals = obs %d / sum %v, want the newest snapshot's 5 / 55", r.ObsCount, r.Sum)
	}
	for i, want := range []float64{3, 1, 1} {
		if got := histogramCounts(t, r.Extra)[i]; got != want {
			t.Errorf("counts[%d] = %v, want %v", i, got, want)
		}
	}
}

// TestMetricAccumulatorDeltaHistogramFolds: a delta point holds only what was
// observed since the last export, so points add up.
func TestMetricAccumulatorDeltaHistogramFolds(t *testing.T) {
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	w := &fakeRollupWriter{}
	acc := NewMetricAccumulator(w, time.Minute, time.Minute, nil)
	acc.now = func() time.Time { return base.Add(90 * time.Second) }

	acc.AddMetric(histogramRecord("delta", uint64(base.Add(5*time.Second).UnixNano()), `[1,2,0]`, 3, 30))
	acc.AddMetric(histogramRecord("delta", uint64(base.Add(15*time.Second).UnixNano()), `[3,1,1]`, 5, 55))

	r := flushOne(t, acc, w)
	if r.ObsCount != 8 || r.Sum != 85 {
		t.Errorf("totals = obs %d / sum %v, want 8 / 85", r.ObsCount, r.Sum)
	}
	for i, want := range []float64{4, 3, 1} {
		if got := histogramCounts(t, r.Extra)[i]; got != want {
			t.Errorf("counts[%d] = %v, want %v", i, got, want)
		}
	}
}

// TestMetricAccumulatorStoresTemporality: compaction cannot tell a running total
// from an increment without it, so it has to reach the row.
func TestMetricAccumulatorStoresTemporality(t *testing.T) {
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	w := &fakeRollupWriter{}
	acc := NewMetricAccumulator(w, time.Minute, time.Minute, nil)
	acc.now = func() time.Time { return base.Add(90 * time.Second) }

	acc.AddMetric(model.MetricRecord{
		ProjectID: 1, Name: "c", Type: model.MetricTypeSum,
		TimeUnixNano: uint64(base.Add(5 * time.Second).UnixNano()),
		Value:        7, Temporality: "cumulative",
		Attributes: json.RawMessage(`{}`),
	})

	if got := flushOne(t, acc, w).Temporality; got != "cumulative" {
		t.Errorf("temporality = %q, want %q", got, "cumulative")
	}
}

func TestMetricAccumulatorSplitsByAttributes(t *testing.T) {
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	w := &fakeRollupWriter{}
	acc := NewMetricAccumulator(w, time.Minute, time.Minute, nil)
	acc.now = func() time.Time { return base.Add(90 * time.Second) }

	acc.AddMetric(gaugeRec("g", `{"svc":"a"}`, uint64(base.Add(5*time.Second).UnixNano()), 1))
	acc.AddMetric(gaugeRec("g", `{"svc":"b"}`, uint64(base.Add(6*time.Second).UnixNano()), 2))

	if err := acc.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(w.got) != 2 {
		t.Fatalf("want 2 series rollups, got %d", len(w.got))
	}
	if w.got[0].AttrFingerprint == w.got[1].AttrFingerprint {
		t.Error("distinct attribute sets should have distinct fingerprints")
	}
}
