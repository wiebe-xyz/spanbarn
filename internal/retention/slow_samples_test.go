package retention

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func slowSpan(trace, name string, at time.Time) repository.Span {
	return repository.Span{ProjectID: 1, Service: "svc", Name: name, TraceID: trace, IngestedAt: at}
}

func TestSlowSamplerCapsTracesPerOperationAndHour(t *testing.T) {
	var p slowSampler
	hour := time.Date(2026, 10, 29, 4, 0, 0, 0, time.UTC)

	for _, c := range []struct {
		span repository.Span
		want bool
	}{
		{slowSpan("a", "op", hour), true},
		{slowSpan("b", "op", hour.Add(time.Minute)), true},
		{slowSpan("c", "op", hour.Add(2*time.Minute)), false},   // third trace of the hour
		{slowSpan("a", "op", hour.Add(3*time.Minute)), true},    // admitted trace stays admitted
		{slowSpan("c", "other", hour.Add(3*time.Minute)), true}, // another operation
		{slowSpan("c", "op", hour.Add(time.Hour)), true},        // the next hour
		{slowSpan("d", "op", hour.Add(4*time.Hour)), true},      // forgets the first hour
		{slowSpan("e", "op", hour.Add(10*time.Minute)), true},   // a forgotten hour counts again
	} {
		if got := p.admit(c.span, 2); got != c.want {
			t.Errorf("admit(%s %s %s) = %v, want %v", c.span.TraceID, c.span.Name, c.span.IngestedAt.Format("15:04"), got, c.want)
		}
	}
	if n := len(p.traces); n > 3 {
		t.Errorf("sampler holds %d scopes, want old hours forgotten", n)
	}
}

func TestRunOnceCopiesSlowOnlySpansUpToTheCap(t *testing.T) {
	worker, repo := setupTestWorker(t, Config{
		InterestingRetentionHours: 1,
		ErrorRetentionDays:        30,
		AggregateRetentionDays:    365,
		SlowThresholdUS:           1_000_000,
		SlowSampleTraces:          5,
	})
	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	insertTestSpans(t, repo, 30, "ok", 5_000_000, old)
	insertTestSpans(t, repo, 3, "error", 500, old)

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var slow, errs int
	if err := repo.DB().QueryRow(
		`SELECT count(*) FILTER (WHERE status = 'ok'), count(*) FILTER (WHERE status = 'error') FROM error_samples`,
	).Scan(&slow, &errs); err != nil {
		t.Fatalf("count error_samples: %v", err)
	}
	if slow != 5 || errs != 3 {
		t.Fatalf("error_samples holds %d slow and %d error spans, want 5 and 3", slow, errs)
	}
}
