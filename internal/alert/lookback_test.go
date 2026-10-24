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

func TestEvaluateReadsOnlyTheWindowTheRuleNeeds(t *testing.T) {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	base := newMockRepo()
	base.alerts = []repository.Alert{{
		ID: 1, ProjectID: 1, Type: "error_rate", Service: "svc",
		ComparisonWindow: 10, CooldownMinutes: 30, Enabled: true, LabelFilters: "{}",
	}}
	repo := &filterRecorder{mockAlertRepo: base}
	eval := NewEvaluator(repo, &mockNotifier{}, nil, nil)
	eval.now = func() time.Time { return now }

	if err := eval.Evaluate(context.Background(), 1); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(repo.filters) != 1 {
		t.Fatalf("aggregate reads = %d, want 1", len(repo.filters))
	}
	got := now.Sub(repo.filters[0].From)
	if got >= aggregateLookback {
		t.Fatalf("lookback = %v, want less than %v for a 10-bucket rule", got, aggregateLookback)
	}
	if want := 11 * bucketAllowance; got < want {
		t.Fatalf("lookback = %v, want at least %v (current + 10 history buckets)", got, want)
	}
}

func TestLookbackForBounds(t *testing.T) {
	if got := lookbackFor(0); got != minLookback {
		t.Errorf("lookbackFor(0) = %v, want %v", got, minLookback)
	}
	if got := lookbackFor(-3); got != minLookback {
		t.Errorf("lookbackFor(-3) = %v, want %v", got, minLookback)
	}
	if got := lookbackFor(100000); got != aggregateLookback {
		t.Errorf("lookbackFor(100000) = %v, want %v", got, aggregateLookback)
	}
}
