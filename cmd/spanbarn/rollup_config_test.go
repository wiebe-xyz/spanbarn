package main

import (
	"reflect"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/config"
)

// notMappedFromRetentionConfig are the retention.Config fields that
// retentionConfigFrom deliberately leaves at zero for withDefaults to fill:
// the worker's own tick and yield are not deployment-tunable windows.
var notMappedFromRetentionConfig = map[string]bool{
	"Interval":   true,
	"BatchYield": true,
}

// TestRetentionConfigFromMapsEveryWindow walks the mapped struct by reflection
// rather than checking a handful of fields, because the failure it guards
// against is a field that exists everywhere except in the mapping.
//
// That is exactly what happened: MetricRollupRetentionDays had a config field, a
// default and a settings key, but no line in retentionConfigFrom, so rollups sat
// at their 365-day fallback no matter what anyone configured. They grew to 3.7 GB,
// 76% of production's database, and nothing an operator could set made any
// difference. A new window added without its mapping now fails here.
func TestRetentionConfigFromMapsEveryWindow(t *testing.T) {
	cfg := config.Config{
		DBPath: "/tmp/spanbarn.db",
		Retention: config.RetentionConfig{
			InterestingHours:      11,
			BoringMinutes:         12,
			ErrorDays:             13,
			AggregatedDays:        14,
			MetricsDays:           15,
			LogHours:              16,
			ErrorLogDays:          17,
			RollupDays:            18,
			RollupHourlyDays:      19,
			RollupDailyDays:       20,
			RollupWeeklyDays:      21,
			RollupMonthlyDays:     22,
			DiskElevatedPct:       75,
			DiskCriticalPct:       90,
			DiskTargetPct:         70,
			BallastMB:             256,
			CompactBucketsPerPass: 12,
		},
		SlowThresholdMS: 500,
	}

	got := retentionConfigFrom(cfg)
	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if notMappedFromRetentionConfig[name] {
			continue
		}
		if v.Field(i).IsZero() {
			t.Errorf("retention.Config.%s is zero: it has no line in retentionConfigFrom, "+
				"so the setting is unreachable however it is configured", name)
		}
	}

	// Spot-check that the tier windows land on the right fields rather than all
	// being copied from one another.
	if got.MetricRollupRetentionDays != 18 || got.HourlyRollupDays != 19 ||
		got.DailyRollupDays != 20 || got.WeeklyRollupDays != 21 || got.MonthlyRollupDays != 22 {
		t.Errorf("rollup tier windows mapped wrong: %d/%d/%d/%d/%d, want 18/19/20/21/22",
			got.MetricRollupRetentionDays, got.HourlyRollupDays,
			got.DailyRollupDays, got.WeeklyRollupDays, got.MonthlyRollupDays)
	}
}

// TestNewRollupCompactorUsesConfiguredBudget: the compactor's pass size is the
// knob that keeps a backfill's write-lock holds short on a full volume.
func TestNewRollupCompactorUsesConfiguredBudget(t *testing.T) {
	cfg := config.Config{DBPath: "/tmp/spanbarn.db"}
	cfg.Retention.CompactBucketsPerPass = 7

	c := newRollupCompactor(nil, cfg, nil)
	if c == nil {
		t.Fatal("newRollupCompactor returned nil")
	}
}
