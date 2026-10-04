package retention

import (
	"context"
	"testing"
	"time"
)

// TestStatsAccumulateDeletesAcrossCycles: the deleted counters are running
// totals across cycles, and the backlog flags describe only the last cycle.
func TestStatsAccumulateDeletesAcrossCycles(t *testing.T) {
	worker, repo := setupTestWorker(t, Config{InterestingRetentionHours: 48, PromptRetentionDays: 30})
	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	insertPromptRecordsAged(t, repo, 3, 31*24*time.Hour)
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	insertPromptRecordsAged(t, repo, 2, 40*24*time.Hour)
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	st := worker.Stats()
	if got := st.Deleted["prompt_records"]; got != 5 {
		t.Errorf("Deleted[prompt_records] = %d, want 5 across two cycles", got)
	}
	if st.Backlog["prompt_records"] {
		t.Error("Backlog[prompt_records] set although the last cycle drained everything")
	}
	for _, table := range RetentionTables {
		if _, ok := st.Deleted[table]; !ok {
			t.Errorf("Deleted has no entry for %q", table)
		}
	}
}

// TestStatsUnmeasuredWithoutDiskProbe: a worker that never sized a volume must
// say so, so the exporter leaves the disk gauges out instead of reporting 0%.
func TestStatsUnmeasuredWithoutDiskProbe(t *testing.T) {
	worker, _ := setupTestWorker(t, Config{InterestingRetentionHours: 48})
	if worker.Stats().Measured {
		t.Fatal("Measured before any cycle")
	}
}

// TestStatsRecordsTierAndSpace: after a cycle on a real volume the tier and
// file sizes are what the ladder saw.
func TestStatsRecordsTierAndSpace(t *testing.T) {
	worker, _ := setupDiskWorker(t, Config{
		InterestingRetentionHours: 48,
		ErrorRetentionDays:        30,
		ErrorLogRetentionDays:     30,
		AggregateRetentionDays:    365,
		// Any real volume is past 0.01% and under 99.9%: tier elevated, no reclaim.
		Watermarks:     Watermarks{Elevated: 0.0001, Critical: 0.999},
		TargetFraction: 0.9995,
	})
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	st := worker.Stats()
	if !st.Measured {
		t.Fatal("Measured = false after a cycle on a file-backed database")
	}
	if st.Tier != TierElevated {
		t.Errorf("Tier = %v, want elevated", st.Tier)
	}
	if st.DBFileBytes <= 0 {
		t.Errorf("DBFileBytes = %d, want > 0", st.DBFileBytes)
	}
	if st.VolumeUsedFraction <= 0 || st.VolumeUsedFraction >= 1 {
		t.Errorf("VolumeUsedFraction = %v, want within (0, 1)", st.VolumeUsedFraction)
	}
}
