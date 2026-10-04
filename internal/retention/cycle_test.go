package retention

import (
	"testing"
	"time"
)

func TestNewCycleCutoffs(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	cut := newCycleCutoffs(now, Config{
		InterestingRetentionHours: 2,
		ErrorRetentionDays:        3,
		AggregateRetentionDays:    4,
		MetricsRetentionDays:      5,
		LogRetentionHours:         6,
		ErrorLogRetentionDays:     7,
	})
	checks := map[string]struct{ got, want time.Time }{
		"interesting": {cut.interesting, now.Add(-2 * time.Hour)},
		"errors":      {cut.errors, now.Add(-72 * time.Hour)},
		"aggregates":  {cut.aggregates, now.Add(-96 * time.Hour)},
		"metrics":     {cut.metrics, now.Add(-120 * time.Hour)},
		"logs":        {cut.logs, now.Add(-6 * time.Hour)},
		"errorLogs":   {cut.errorLogs, now.Add(-168 * time.Hour)},
	}
	for name, c := range checks {
		if !c.got.Equal(c.want) {
			t.Errorf("%s cutoff = %v, want %v", name, c.got, c.want)
		}
	}
}

func TestCycleStatsReportsEveryCounter(t *testing.T) {
	st := cycleStats{spansDeleted: 4, backlogRemains: true}
	attrs := st.attributes()
	args := st.logArgs()
	if len(attrs) != 19 {
		t.Fatalf("attributes = %d, want 19", len(attrs))
	}
	if len(args) != 38 {
		t.Fatalf("log args = %d, want 38 (19 key/value pairs)", len(args))
	}
}
