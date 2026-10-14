package api

import (
	"testing"
	"time"
)

func TestNextDashboardWarmLandsJustAfterTheNextBoundary(t *testing.T) {
	now := time.Date(2026, 10, 15, 9, 37, 41, 0, time.UTC)
	got := nextDashboardWarm(now, 5*time.Second)
	want := time.Date(2026, 10, 15, 9, 45, 8, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("nextDashboardWarm = %s, want %s", got, want)
	}

	// Exactly on a boundary the next warm is a full period away, never now.
	onBoundary := time.Date(2026, 10, 15, 9, 45, 0, 0, time.UTC)
	if got := nextDashboardWarm(onBoundary, 0); !got.After(onBoundary) {
		t.Errorf("warm on a boundary = %s, want after %s", got, onBoundary)
	}
}
