package service

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// seedEnvironmentRoots stores root spans in three environments: two in
// production, one in staging and one that names none.
func seedEnvironmentRoots(t *testing.T, repo *repository.Repository) {
	t.Helper()
	span := func(id, attrs string) repository.Span {
		return repository.Span{ProjectID: 1, TraceID: "t-" + id, SpanID: id, Name: "GET /", Service: "web", Kind: "server",
			Status: "ok", StartTimeUs: time.Now().UnixMicro(), DurationUs: 1000, Attributes: attrs, Events: "[]"}
	}
	if err := repo.InsertSpans([]repository.Span{
		span("p1", `{"deployment.environment":"production"}`),
		span("p2", `{"deployment.environment":"production"}`),
		span("s1", `{"deployment.environment":"staging"}`),
		span("n1", `{}`),
	}); err != nil {
		t.Fatal(err)
	}
}

func inEnvironment(env string) context.Context {
	return filter.WithEnvironmentScope(context.Background(), env)
}

// Each environment's dashboard cards and trace groups read that environment's
// spans, and a cached answer for one environment is never served for another.
func TestDashboardAndTraceGroupsReadOneEnvironment(t *testing.T) {
	repo := setupTestRepo(t)
	seedEnvironmentRoots(t, repo)
	svc := NewQueryService(repo, nil, nil)
	svc.SetCache(cache.New(cache.NewMemoryStore(), time.Minute))
	from, to := dashboardRange()
	q := DashboardQuery{ProjectID: 1, From: from, To: to}

	// The order walks each environment twice, so a second read of one answers
	// from the cache the first filled and the entries still stay apart.
	want := map[string]int64{"": 4, "production": 3, "staging": 1, "testing": 0}
	for _, env := range []string{"", "production", "staging", "testing", "staging", "production", ""} {
		ctx := context.Background()
		if env != "" {
			ctx = inEnvironment(env)
		}
		counts, err := svc.GetDashboardCounts(ctx, q, "service", true)
		if err != nil {
			t.Fatalf("environment %q: %v", env, err)
		}
		var n int64
		for _, p := range counts.Points {
			n += p.Count
		}
		if n != want[env] {
			t.Errorf("environment %q: dashboard counts %d, want %d", env, n, want[env])
		}

		groups, err := svc.ListTraceGroups(ctx, TraceSearchFilter{ProjectID: 1, To: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatalf("environment %q: %v", env, err)
		}
		var g int64
		for _, grp := range groups {
			g += grp.Count
		}
		if g != want[env] {
			t.Errorf("environment %q: trace groups %d, want %d", env, g, want[env])
		}
	}
}

// environmentFixture is the rollup fixture with each trace in one of three
// environments: production, staging, and none, which reads as production.
func environmentFixture() []repository.Span {
	spans := rollupFixture()
	envs := []string{`"deployment.environment":"production"`, `"deployment.environment":"staging"`, ""}
	for i := range spans {
		env := envs[(i/2)%len(envs)]
		attrs := spans[i].Attributes
		switch {
		case env == "":
		case attrs == "{}":
			spans[i].Attributes = "{" + env + "}"
		default:
			spans[i].Attributes = attrs[:len(attrs)-1] + "," + env + "}"
		}
	}
	return spans
}

// An environment's dashboard cards and trace groups answer the same from the
// span rollups as from raw spans, and the environments split what the
// unscoped read adds up.
func TestEnvironmentReadsRollupsMatchRawSpans(t *testing.T) {
	repo := setupTestRepo(t)
	seedRollupFixture(t, repo, environmentFixture())
	rollups := NewQueryService(repo, nil, nil)
	rollups.SetSpanRollupReads(SpanRollupReads{Enabled: true, HTTPStatus: true})
	spans := NewQueryService(repo, nil, nil)
	q := DashboardQuery{From: rollupBase, To: rollupBase.Add(2 * time.Hour)}
	f := TraceSearchFilter{From: rollupBase, To: rollupBase.Add(2 * time.Hour)}

	total := func(c *DashboardCounts) (n int64) {
		for _, p := range c.Points {
			n += p.Count
		}
		return n
	}
	sums := map[string]int64{}
	for _, env := range []string{"production", "staging", ""} {
		ctx := context.Background()
		if env != "" {
			ctx = inEnvironment(env)
		}
		got, err := rollups.GetDashboardCounts(ctx, q, "service", false)
		if err != nil {
			t.Fatal(err)
		}
		want, err := spans.GetDashboardCounts(ctx, q, "service", false)
		if err != nil {
			t.Fatal(err)
		}
		wantSource(t, env+" counts", got.Source, SourceRollups)
		wantSource(t, env+" counts", want.Source, SourceSpans)
		if total(want) == 0 || !reflect.DeepEqual(got.Points, want.Points) {
			t.Errorf("environment %q counts:\nrollups %+v\nspans   %+v", env, got.Points, want.Points)
		}
		sums[env] = total(got)

		gg, err := rollups.ListTraceGroups(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		wg, err := spans.ListTraceGroups(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(wg) == 0 || len(gg) != len(wg) {
			t.Fatalf("environment %q groups:\nrollups %+v\nspans   %+v", env, gg, wg)
		}
		for i := range wg {
			gg[i].Source = wg[i].Source
			if !reflect.DeepEqual(gg[i], wg[i]) {
				t.Errorf("environment %q group %d:\nrollups %+v\nspans   %+v", env, i, gg[i], wg[i])
			}
		}
	}
	if sums["production"]+sums["staging"] != sums[""] || sums["staging"] == 0 || sums["production"] <= sums["staging"] {
		t.Errorf("environment sums %v: production plus staging must equal the unscoped read", sums)
	}
}
