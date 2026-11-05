package rollup

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// TestCompactorKeepsCarryWithinProject: three projects hold one metric name and
// fingerprint. Each hour row has to measure its increase from that project's own
// carry row, and a project with no carry row contributes nothing.
func TestCompactorKeepsCarryWithinProject(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	h := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	row := func(project int64, bucket time.Time, last float64) repository.MetricRollup {
		r := counterRow(bucket, "v1", last)
		r.ProjectID = project
		return r
	}
	rows := []repository.MetricRollup{
		row(1, h.Add(-5*time.Minute), 10), row(1, h, 20),
		row(2, h.Add(-5*time.Minute), 5000), row(2, h, 5100),
		row(3, h, 7000),
	}
	if err := repo.UpsertMetricRollups(rows); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}

	now := h.Add(3 * time.Hour)
	if err := newTestCompactor(t, repo, now).RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	for project, want := range map[int64]float64{1: 10, 2: 100, 3: 0} {
		got, err := repo.QueryCoarseRollups(ctx, repository.CoarseRollupFilter{
			ProjectID: project, Name: "reqs", StepSeconds: StepHour, From: h, To: now,
		})
		if err != nil {
			t.Fatalf("QueryCoarseRollups: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("project %d: got %d rows, want 1", project, len(got))
		}
		if got[0].Sum != want {
			t.Errorf("project %d increase = %v, want %v", project, got[0].Sum, want)
		}
	}
}
