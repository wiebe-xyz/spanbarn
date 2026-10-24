package alert

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// filterRecorder remembers the aggregate filter of each read.
type filterRecorder struct {
	*mockAlertRepo
	filters []repository.AggregateFilter
}

func (f *filterRecorder) QueryAggregates(filter repository.AggregateFilter) ([]repository.Aggregate, error) {
	f.filters = append(f.filters, filter)
	return f.mockAlertRepo.QueryAggregates(filter)
}

// lookbackRead runs one evaluation of a rule with the given comparison window
// and returns how far back it read aggregates.
func lookbackRead(t *testing.T, window int, interval time.Duration) time.Duration {
	t.Helper()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	base := newMockRepo()
	base.alerts = []repository.Alert{{
		ID: 1, ProjectID: 1, Type: "error_rate", Service: "svc",
		ComparisonWindow: window, CooldownMinutes: 30, Enabled: true, LabelFilters: "{}",
	}}
	repo := &filterRecorder{mockAlertRepo: base}
	eval := NewEvaluator(repo, &mockNotifier{}, nil, nil)
	eval.now = func() time.Time { return now }
	if interval > 0 {
		eval.SetAggregateInterval(interval)
	}
	if err := eval.Evaluate(context.Background(), 1); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(repo.filters) != 1 {
		t.Fatalf("aggregate reads = %d, want 1", len(repo.filters))
	}
	return now.Sub(repo.filters[0].From)
}

func TestEvaluateReadsOnlyTheWindowTheRuleNeeds(t *testing.T) {
	// One-minute buckets (the default): 11 buckets fit in the 1h floor.
	if got := lookbackRead(t, 10, 0); got != minLookback {
		t.Fatalf("1m interval: lookback = %v, want %v", got, minLookback)
	}
	// One-hour buckets: current + 10 history buckets need 11h, well under 24h.
	if got, want := lookbackRead(t, 10, time.Hour), 11*time.Hour; got != want {
		t.Fatalf("1h interval: lookback = %v, want %v", got, want)
	}
	// Five-minute buckets with a big window: 31 x 5m.
	if got, want := lookbackRead(t, 30, 5*time.Minute), 31*5*time.Minute; got != want {
		t.Fatalf("5m interval: lookback = %v, want %v", got, want)
	}
}

func TestLookbackForBounds(t *testing.T) {
	if got := lookbackFor(0, time.Minute); got != minLookback {
		t.Errorf("lookbackFor(0, 1m) = %v, want %v", got, minLookback)
	}
	if got := lookbackFor(-3, time.Minute); got != minLookback {
		t.Errorf("lookbackFor(-3, 1m) = %v, want %v", got, minLookback)
	}
	if got := lookbackFor(100000, time.Hour); got != aggregateLookback {
		t.Errorf("lookbackFor(100000, 1h) = %v, want %v", got, aggregateLookback)
	}
	if got := lookbackFor(30, time.Hour); got != aggregateLookback {
		t.Errorf("lookbackFor(30, 1h) = %v, want the %v ceiling", got, aggregateLookback)
	}
}
