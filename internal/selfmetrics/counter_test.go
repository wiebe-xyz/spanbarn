package selfmetrics

import (
	"log/slog"
	"testing"
	"time"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func exportedMetrics(t *testing.T, rec *Recorder) map[string]*metricspb.Metric {
	t.Helper()
	rp := NewReporter(rec, "http://unused", "", time.Minute, nil, 1, slog.Default())
	req := rp.buildRequest(rec.snapshot(), 2)
	out := map[string]*metricspb.Metric{}
	for _, m := range req.ResourceMetrics[0].ScopeMetrics[0].Metrics {
		out[m.Name] = m
	}
	return out
}

// TestRegisterCounterExportsCumulativeSum: a registered counter must arrive as
// a monotonic cumulative sum, so rate() over it means rows per second.
func TestRegisterCounterExportsCumulativeSum(t *testing.T) {
	rec := NewRecorder()
	rec.RegisterCounter("spanbarn.retention.deleted", map[string]string{"table": "logs"}, func() float64 { return 42 })

	m := exportedMetrics(t, rec)["spanbarn.retention.deleted"]
	if m == nil {
		t.Fatal("counter not exported")
	}
	sum := m.GetSum()
	if sum == nil || !sum.IsMonotonic ||
		sum.AggregationTemporality != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
		t.Fatalf("counter exported as %v, want a cumulative monotonic sum", m.Data)
	}
	dp := sum.DataPoints[0]
	if dp.GetAsDouble() != 42 || dp.StartTimeUnixNano != 1 {
		t.Errorf("data point = %v (start %d), want 42 from start 1", dp.GetAsDouble(), dp.StartTimeUnixNano)
	}
}

// TestOptionalGaugeOmittedUntilKnown: a reading that has not been taken must be
// left out, not exported as zero.
func TestOptionalGaugeOmittedUntilKnown(t *testing.T) {
	known := false
	rec := NewRecorder()
	rec.RegisterOptionalGauge("spanbarn.disk.used_pct", nil, func() (float64, bool) { return 61, known })

	if _, ok := exportedMetrics(t, rec)["spanbarn.disk.used_pct"]; ok {
		t.Fatal("unknown reading was exported")
	}
	known = true
	m := exportedMetrics(t, rec)["spanbarn.disk.used_pct"]
	if m == nil || m.GetGauge().DataPoints[0].GetAsDouble() != 61 {
		t.Fatalf("known reading = %v, want gauge 61", m)
	}
}
