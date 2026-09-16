package api

import (
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/rollup"
)

// TestRollupTierStepsPicksResolutionForRange: each range reads the finest tier
// that still gives a readable chart, with coarser tiers behind it as fallbacks.
// The fallbacks matter because the fine tiers are kept for days while the coarse
// ones go back years, so an old range has no fine buckets left to read.
func TestRollupTierStepsPicksResolutionForRange(t *testing.T) {
	tests := []struct {
		name     string
		width    time.Duration
		wantHead int64
		wantLen  int
	}{
		{"just past the raw threshold", 7 * time.Hour, rollup.Step5m, 3},
		{"two days", 48 * time.Hour, rollup.Step5m, 3},
		{"a week", 7 * 24 * time.Hour, rollup.StepHour, 3},
		{"thirty days", 30 * 24 * time.Hour, rollup.StepHour, 3},
		{"three months", 90 * 24 * time.Hour, rollup.StepDay, 3},
		{"a year", 365 * 24 * time.Hour, rollup.StepDay, 3},
		{"three years", 3 * 365 * 24 * time.Hour, rollup.StepWeek, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rollupTierSteps(tc.width)
			if len(got) != tc.wantLen {
				t.Fatalf("steps = %v, want %d entries", got, tc.wantLen)
			}
			if got[0] != tc.wantHead {
				t.Errorf("first choice = %d, want %d", got[0], tc.wantHead)
			}
			for i := 1; i < len(got); i++ {
				if got[i] <= got[i-1] {
					t.Errorf("fallbacks must get coarser, got %v", got)
				}
			}
		})
	}
}
