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

func TestMetricAccumulatorHistogramMerge(t *testing.T) {
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	w := &fakeRollupWriter{}
	acc := NewMetricAccumulator(w, time.Minute, time.Minute, nil)
	acc.now = func() time.Time { return base.Add(90 * time.Second) }

	hist := func(tNano uint64, counts string, obs uint64, sum float64) model.MetricRecord {
		return model.MetricRecord{
			ProjectID: 1, Name: "h", Type: model.MetricTypeHistogram,
			TimeUnixNano: tNano, Value: sum, Count: obs,
			Attributes: json.RawMessage(`{}`),
			Extra:      json.RawMessage(`{"bounds":[10,20],"counts":` + counts + `}`),
		}
	}
	acc.AddMetric(hist(uint64(base.Add(5*time.Second).UnixNano()), `[1,2,0]`, 3, 30))
	acc.AddMetric(hist(uint64(base.Add(15*time.Second).UnixNano()), `[3,1,1]`, 5, 55))

	if err := acc.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(w.got) != 1 {
		t.Fatalf("want 1 rollup, got %d", len(w.got))
	}
	r := w.got[0]
	if r.ObsCount != 8 || r.Sum != 85 {
		t.Errorf("histogram totals wrong: obs=%d sum=%v", r.ObsCount, r.Sum)
	}
	var merged struct {
		Bounds []float64 `json:"bounds"`
		Counts []float64 `json:"counts"`
	}
	if err := json.Unmarshal([]byte(r.Extra), &merged); err != nil {
		t.Fatalf("unmarshal extra: %v", err)
	}
	want := []float64{4, 3, 1}
	for i, c := range want {
		if merged.Counts[i] != c {
			t.Errorf("merged counts[%d] = %v, want %v (%v)", i, merged.Counts[i], c, merged.Counts)
		}
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
