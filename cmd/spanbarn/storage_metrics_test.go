package main

import (
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/retention"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
)

func readingsByKey(rec *selfmetrics.Recorder) map[string]float64 {
	out := map[string]float64{}
	for _, r := range rec.Readings() {
		key := r.Name
		if table := r.Attrs["table"]; table != "" {
			key += "{" + table + "}"
		}
		out[key] = r.Value
	}
	return out
}

// TestStorageMetricsWaitForFirstMeasurement: before retention has sized the
// volume the disk gauges are absent, so a fresh pod does not report 0% used.
// The per-table series are present from the start.
func TestStorageMetricsWaitForFirstMeasurement(t *testing.T) {
	stats := retention.Stats{Deleted: map[string]int64{}, Backlog: map[string]bool{}}
	rec := selfmetrics.NewRecorder()
	registerStorageMetrics(rec, func() retention.Stats { return stats })

	got := readingsByKey(rec)
	if _, ok := got["spanbarn.disk.used_pct"]; ok {
		t.Error("disk.used_pct exported before any measurement")
	}
	if want := len(retention.RetentionTables) + len(retention.BacklogTables); len(got) != want {
		t.Errorf("unmeasured: %d readings, want %d per-table series", len(got), want)
	}

	stats = retention.Stats{
		Measured:           true,
		Tier:               retention.TierElevated,
		VolumeUsedFraction: 0.42,
		DBFileBytes:        4 << 30,
		Deleted:            map[string]int64{"prompt_records": 20000},
		Backlog:            map[string]bool{"prompt_records": true},
	}
	got = readingsByKey(rec)
	checks := map[string]float64{
		"spanbarn.disk.tier":                         1,
		"spanbarn.disk.used_pct":                     42,
		"spanbarn.db.bytes":                          4 << 30,
		"spanbarn.retention.deleted{prompt_records}": 20000,
		"spanbarn.retention.backlog{prompt_records}": 1,
		"spanbarn.retention.backlog{spans}":          0,
	}
	for key, want := range checks {
		if v, ok := got[key]; !ok || v != want {
			t.Errorf("%s = %v (present %v), want %v", key, v, ok, want)
		}
	}
}
