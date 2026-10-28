package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// fixtureStats folds the rollup fixture the way the rollups should: spans
// per key, errors, and a histogram of their durations.
func fixtureStats(keep func(repository.Span) bool, key func(repository.Span) string) map[string]*rollupFold {
	out := map[string]*rollupFold{}
	for _, s := range rollupFixture() {
		if !keep(s) {
			continue
		}
		f := out[key(s)]
		if f == nil {
			f = &rollupFold{}
			out[key(s)] = f
		}
		f.effective++
		if s.Status == "error" {
			f.errorCount++
		}
		var h spanhist.Hist
		h.Observe(s.DurationUs)
		f.hist.Merge(h)
	}
	return out
}

func TestServicesFromRollups(t *testing.T) {
	rollups, _ := rollupServices(t)
	ctx := context.Background()
	to := rollupBase.Add(2 * time.Hour)
	for _, serverOnly := range []bool{false, true} {
		got, err := rollups.ListServices(ctx, 1, rollupBase, to, serverOnly)
		if err != nil {
			t.Fatal(err)
		}
		want := fixtureStats(func(s repository.Span) bool { return !serverOnly || s.Kind == "server" },
			func(s repository.Span) string { return s.Service })
		if len(got) != len(want) {
			t.Fatalf("serverOnly=%v: %d services, want %d: %+v", serverOnly, len(got), len(want), got)
		}
		for _, g := range got {
			w := want[g.Service]
			wantSource(t, "services", g.Source, SourceRollups)
			if w == nil || g.SpanCount != w.effective || g.ErrorCount != w.errorCount || g.P95Us != w.hist.Percentile(95) {
				t.Errorf("serverOnly=%v %s: got %+v, want %d spans, %d errors, p95 %d",
					serverOnly, g.Service, g, w.effective, w.errorCount, w.hist.Percentile(95))
			}
		}
	}
}

func TestTimeseriesFromRollups(t *testing.T) {
	rollups, _ := rollupServices(t)
	ctx := context.Background()
	to := rollupBase.Add(2 * time.Hour)
	for _, tc := range []struct {
		operation string
		interval  time.Duration
	}{{"GET /", 0}, {"GET /", 15 * time.Minute}, {allOperations, 0}} {
		got, err := rollups.GetTimeseries(ctx, 1, "web", tc.operation, rollupBase, to, tc.interval)
		if err != nil {
			t.Fatal(err)
		}
		interval := timeseriesInterval(rollupBase, to, tc.interval)
		want := fixtureStats(func(s repository.Span) bool {
			return s.Service == "web" && (tc.operation == allOperations || s.Name == tc.operation)
		}, func(s repository.Span) string { return s.IngestedAt.Truncate(interval).UTC().String() })
		if len(got) == 0 || len(got) != len(want) {
			t.Fatalf("%s every %v: %d buckets, want %d", tc.operation, interval, len(got), len(want))
		}
		for _, b := range got {
			w := want[b.Bucket.String()]
			wantSource(t, "timeseries", b.Source, SourceRollups)
			if w == nil || b.Count != w.effective || b.ErrorCount != w.errorCount || b.P50Us != w.hist.Percentile(50) {
				t.Errorf("%s every %v, bucket %v: got %+v", tc.operation, interval, b.Bucket, b)
			}
		}
	}
}

// Windows and chart buckets the rollups cannot answer go to the aggregates.
func TestServicesAndTimeseriesRollupSourceSelection(t *testing.T) {
	rollups, _ := rollupServices(t)
	ctx := context.Background()
	to := rollupBase.Add(2 * time.Hour)
	if _, ok, _ := rollups.servicesFromRollups(ctx, 1, to.Add(-30*time.Minute), to, ""); ok {
		t.Error("services read rollups for a 30-minute window")
	}
	if _, ok, _ := rollups.servicesFromRollups(ctx, 1, rollupBase.Add(-time.Hour), to, ""); ok {
		t.Error("services read rollups before the oldest bucket")
	}
	if _, ok, _ := rollups.timeseriesFromRollups(ctx, 1, "web", "GET /", rollupBase, to, time.Minute); ok {
		t.Error("timeseries read rollups for 1-minute buckets")
	}
	if got := timeseriesInterval(to.Add(-10*time.Minute), to, 0); got != time.Minute {
		t.Errorf("interval for 10 minutes = %v, want 1m", got)
	}
}

// A 5-minute bucket without rows in the middle of the window sends every
// rollup read to its other source, though Plan alone counts it covered.
func TestRollupGapReadsTheOtherSource(t *testing.T) {
	rollups, _ := rollupServices(t)
	repo := rollups.repo.(*repository.Repository)
	ctx := context.Background()
	to := rollupBase.Add(2 * time.Hour)
	if _, err := repo.DB().Exec(`DELETE FROM span_rollups WHERE step_seconds = 300 AND bucket = ?`,
		rollupBase.Add(10*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	gapped := NewQueryService(repo, nil, nil)
	gapped.SetSpanRollupReads(SpanRollupReads{Enabled: true, HTTPStatus: true})

	counts, err := gapped.GetDashboardCounts(ctx, DashboardQuery{From: rollupBase, To: to}, "service", false)
	if err != nil {
		t.Fatal(err)
	}
	wantSource(t, "dashboard across the gap", counts.Source, SourceSpans)
	groups, err := gapped.ListTraceGroups(ctx, TraceSearchFilter{From: rollupBase, To: to})
	if err != nil || len(groups) == 0 {
		t.Fatalf("trace groups: %v, %v", groups, err)
	}
	wantSource(t, "trace groups across the gap", groups[0].Source, SourceSpans)
	if _, ok, _ := gapped.servicesFromRollups(ctx, 1, rollupBase, to, ""); ok {
		t.Error("services read rollups across the gap")
	}

	after, err := gapped.GetDashboardCounts(ctx, DashboardQuery{From: rollupBase.Add(15 * time.Minute), To: to}, "service", false)
	if err != nil {
		t.Fatal(err)
	}
	wantSource(t, "dashboard after the gap", after.Source, SourceRollups)
}
