package service

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

var rollupBase = time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)

// rollupFixture is two hours of unweighted spans, so the spans the writer
// received are the spans it stored and both sources must agree.
func rollupFixture() []repository.Span {
	var spans []repository.Span
	durations := []int64{120, 900, 4100, 35000, 260000}
	for i := range 48 {
		at := rollupBase.Add(time.Duration(i)*150*time.Second + 7*time.Second)
		root := repository.Span{
			ProjectID: 1, TraceID: fmt.Sprintf("t%d", i), SpanID: fmt.Sprintf("r%d", i),
			Name: []string{"GET /", "POST /pay", "GET /health"}[i%3], Service: []string{"web", "api"}[i%2],
			Kind: "server", Status: "ok", DurationUs: durations[i%len(durations)], IngestedAt: at,
			Attributes: `{"http.response.status_code":200}`, Events: "[]",
		}
		if i%5 == 0 {
			root.Status, root.Attributes = "error", `{"http.response.status_code":503}`
		}
		child := repository.Span{
			ProjectID: 1, TraceID: root.TraceID, SpanID: "c" + root.SpanID, ParentSpanID: root.SpanID,
			Name: "SELECT", Service: "db", Kind: "client", Status: "ok", DurationUs: durations[(i+2)%len(durations)] / 3,
			IngestedAt: at, Attributes: "{}", Events: "[]",
		}
		spans = append(spans, root, child)
	}
	return spans
}

// seedRollupFixture stores spans as raw spans with their arrival times and
// as span rollups through the writer's accumulator.
func seedRollupFixture(t *testing.T, repo *repository.Repository, spans []repository.Span) {
	t.Helper()
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
	for _, s := range spans {
		if _, err := repo.DB().Exec(`UPDATE spans SET ingested_at = ? WHERE span_id = ?`, s.IngestedAt.UTC().String(), s.SpanID); err != nil {
			t.Fatal(err)
		}
	}
	r := aggregation.NewSpanRollups(repo, aggregation.SpanRollupConfig{HTTPStatus: true}, nil)
	for _, s := range spans {
		r.Add(s)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
}

func rollupServices(t *testing.T) (rollups, spans *QueryService) {
	t.Helper()
	repo := setupTestRepo(t)
	seedRollupFixture(t, repo, rollupFixture())
	rollups = NewQueryService(repo, nil, nil)
	rollups.SetSpanRollupReads(SpanRollupReads{Enabled: true, HTTPStatus: true})
	return rollups, NewQueryService(repo, nil, nil)
}

func wantSource(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: source = %q, want %q", what, got, want)
	}
}

// Over unweighted spans the rollups answer every dashboard card exactly as
// the raw spans do.
func TestDashboardRollupsMatchRawSpans(t *testing.T) {
	rollups, spans := rollupServices(t)
	ctx := context.Background()
	q := DashboardQuery{From: rollupBase, To: rollupBase.Add(2 * time.Hour)}
	banded := q
	banded.MinDurationUs, banded.MaxDurationUs = repository.HeatmapBucketLowerUs(18), repository.HeatmapBucketLowerUs(32)

	for _, tc := range []struct {
		group string
		root  bool
	}{{"service", false}, {"name", false}, {"status", false}, {"service", true}, {"http_status", true}} {
		for _, dq := range []DashboardQuery{q, banded} {
			name := fmt.Sprintf("counts %s root=%v band=%d-%d", tc.group, tc.root, dq.MinDurationUs, dq.MaxDurationUs)
			got, err := rollups.GetDashboardCounts(ctx, dq, tc.group, tc.root)
			if err != nil {
				t.Fatal(err)
			}
			want, err := spans.GetDashboardCounts(ctx, dq, tc.group, tc.root)
			if err != nil {
				t.Fatal(err)
			}
			wantSource(t, name, got.Source, SourceRollups)
			wantSource(t, name, want.Source, SourceSpans)
			if len(want.Points) == 0 || !reflect.DeepEqual(got.Points, want.Points) {
				t.Errorf("%s:\nrollups %+v\nspans   %+v", name, got.Points, want.Points)
			}
		}
	}

	for _, root := range []bool{false, true} {
		got, err := rollups.GetDashboardHeatmap(ctx, q, root)
		if err != nil {
			t.Fatal(err)
		}
		want, err := spans.GetDashboardHeatmap(ctx, q, root)
		if err != nil {
			t.Fatal(err)
		}
		wantSource(t, "heatmap", got.Source, SourceRollups)
		if len(want.Cells) == 0 || !reflect.DeepEqual(got.Cells, want.Cells) {
			t.Errorf("heatmap root=%v:\nrollups %+v\nspans   %+v", root, got.Cells, want.Cells)
		}
	}

	got, err := rollups.GetDashboardPercentiles(ctx, q, "name")
	if err != nil {
		t.Fatal(err)
	}
	want, err := spans.GetDashboardPercentiles(ctx, q, "name")
	if err != nil {
		t.Fatal(err)
	}
	wantSource(t, "percentiles", got.Source, SourceRollups)
	if len(want.Points) == 0 || !reflect.DeepEqual(got.Points, want.Points) {
		t.Errorf("percentiles:\nrollups %+v\nspans   %+v", got.Points, want.Points)
	}
}

func TestTraceGroupsRollupsMatchRawSpans(t *testing.T) {
	rollups, spans := rollupServices(t)
	ctx := context.Background()
	f := TraceSearchFilter{From: rollupBase, To: rollupBase.Add(2 * time.Hour)}
	got, err := rollups.ListTraceGroups(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	want, err := spans.ListTraceGroups(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || len(got) != len(want) {
		t.Fatalf("rollups %+v\nspans   %+v", got, want)
	}
	for i := range want {
		wantSource(t, "trace groups", got[i].Source, SourceRollups)
		wantSource(t, "trace groups", want[i].Source, SourceSpans)
		got[i].Source = want[i].Source
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("group %d:\nrollups %+v\nspans   %+v", i, got[i], want[i])
		}
	}
}

// A read the rollups cannot answer exactly reads raw spans.
func TestDashboardRollupSourceSelection(t *testing.T) {
	rollups, _ := rollupServices(t)
	off := NewQueryService(rollups.repo, nil, nil)
	off.SetSpanRollupReads(SpanRollupReads{HTTPStatus: true})
	noHTTP := NewQueryService(rollups.repo, nil, nil)
	noHTTP.SetSpanRollupReads(SpanRollupReads{Enabled: true})
	ctx := context.Background()
	q := DashboardQuery{From: rollupBase, To: rollupBase.Add(2 * time.Hour)}
	with := func(f func(*DashboardQuery)) DashboardQuery { c := q; f(&c); return c }

	for _, tc := range []struct {
		name  string
		svc   *QueryService
		q     DashboardQuery
		group string
		root  bool
		want  string
	}{
		{"covered", rollups, q, "service", false, SourceRollups},
		{"an hour reads 1-minute chart buckets", rollups, with(func(c *DashboardQuery) { c.From = c.To.Add(-time.Hour) }), "service", false, SourceSpans},
		{"before the oldest rollup", rollups, with(func(c *DashboardQuery) { c.From = c.From.Add(-time.Hour) }), "service", false, SourceSpans},
		{"a status the rollups do not keep", rollups, with(func(c *DashboardQuery) { c.Status = "OK" }), "service", false, SourceSpans},
		{"a band off the bucket edges", rollups, with(func(c *DashboardQuery) { c.MinDurationUs = 1000 }), "service", false, SourceSpans},
		{"http status of every span", rollups, q, "http_status", false, SourceSpans},
		{"http status of root spans", rollups, q, "http_status", true, SourceRollups},
		{"http status split off", noHTTP, q, "http_status", true, SourceSpans},
		{"reads off", off, q, "service", false, SourceSpans},
	} {
		got, err := tc.svc.GetDashboardCounts(ctx, tc.q, tc.group, tc.root)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		wantSource(t, tc.name, got.Source, tc.want)
	}
}

func TestTraceGroupsRollupSourceSelection(t *testing.T) {
	rollups, _ := rollupServices(t)
	ctx := context.Background()
	f := TraceSearchFilter{From: rollupBase, To: rollupBase.Add(2 * time.Hour)}
	with := func(fn func(*TraceSearchFilter)) TraceSearchFilter { c := f; fn(&c); return c }
	for _, tc := range []struct {
		name string
		f    TraceSearchFilter
		want string
	}{
		{"covered", f, SourceRollups},
		{"under an hour", with(func(c *TraceSearchFilter) { c.From = c.To.Add(-30 * time.Minute) }), SourceSpans},
		{"a minimum duration", with(func(c *TraceSearchFilter) { c.MinDurationUs = 1000 }), SourceSpans},
		{"error groups", with(func(c *TraceSearchFilter) { c.Status = "error" }), SourceSpans},
	} {
		groups, err := rollups.ListTraceGroups(ctx, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(groups) == 0 {
			t.Fatalf("%s: no groups", tc.name)
		}
		wantSource(t, tc.name, groups[0].Source, tc.want)
	}
}
