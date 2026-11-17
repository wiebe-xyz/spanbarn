package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

// envSpans stores one root span per environment in one bucket: production by
// its attribute, staging, and one that names none. All three read as the
// spans of one project and one operation.
func envSpans(t *testing.T, repo *Repository, at time.Time) {
	t.Helper()
	dashSpan(t, repo, "p", "", "GET /", "web", `{"deployment.environment":"production"}`, 10, at)
	dashSpan(t, repo, "s1", "", "GET /", "web", `{"deployment.environment":"staging"}`, 10, at)
	dashSpan(t, repo, "s2", "", "GET /", "web", `{"deployment.environment":"staging"}`, 10, at)
	dashSpan(t, repo, "n", "", "GET /", "web", `{}`, 10, at)
}

func countOf(pts []DashboardCountPoint) int64 {
	var n int64
	for _, p := range pts {
		n += p.Count
	}
	return n
}

// The dashboard cards read one environment's spans, and production also reads
// the spans that name none.
func TestDashboardCountsEnvironment(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	envSpans(t, repo, at)

	want := map[string]int64{"": 4, "all": 4, "production": 2, "staging": 2, "testing": 0}
	for env, n := range want {
		pts, err := repo.QueryDashboardCounts(context.Background(), SpanFilter{ProjectID: 1, Environment: env}, 3600, DashboardGroupService, 10)
		if err != nil {
			t.Fatalf("environment %q: %v", env, err)
		}
		if got := countOf(pts); got != n {
			t.Errorf("environment %q: %d spans, want %d", env, got, n)
		}
	}
}

// Percentiles and the heatmap share the dashboard where builder, so one
// environment's spans leave the other's out of both.
func TestDashboardPercentilesAndHeatmapEnvironment(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dashSpan(t, repo, "p", "", "GET /", "web", `{"deployment.environment":"production"}`, 10, at)
	dashSpan(t, repo, "s", "", "GET /", "web", `{"deployment.environment":"staging"}`, 90_000, at)

	pts, err := repo.QueryDashboardPercentiles(context.Background(), SpanFilter{ProjectID: 1, Environment: "staging"}, 3600, DashboardGroupService, 10)
	if err != nil || len(pts) != 1 || pts[0].Count != 1 || pts[0].P95Us < 50_000 {
		t.Fatalf("staging percentiles = %+v, %v", pts, err)
	}
	cells, err := repo.QueryDashboardHeatmap(context.Background(), SpanFilter{ProjectID: 1, Environment: "production"}, 3600)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, c := range cells {
		n += c.Count
		if c.DurationBucket > 10 {
			t.Errorf("production heatmap holds a slow span: bucket %d", c.DurationBucket)
		}
	}
	if n != 1 {
		t.Errorf("production heatmap holds %d spans, want 1", n)
	}
}

// Root span groups count one environment's root spans.
func TestRootSpanGroupsEnvironment(t *testing.T) {
	repo := setupTestDB(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	envSpans(t, repo, at)

	want := map[string]int64{"": 4, "production": 2, "staging": 2, "testing": 0}
	for env, n := range want {
		groups, err := repo.QueryRootSpanGroups(context.Background(), SpanFilter{ProjectID: 1, Environment: env})
		if err != nil {
			t.Fatalf("environment %q: %v", env, err)
		}
		var got int64
		for _, g := range groups {
			got += g.Count
		}
		if got != n {
			t.Errorf("environment %q: %d root spans, want %d", env, got, n)
		}
	}
}

// The environment condition compiles to a real predicate. A failed compile
// selects nothing, so this pins the form the dashboard and the groups rely on.
func TestAppendEnvironmentWhere(t *testing.T) {
	for _, env := range []string{"production", "staging"} {
		where, args := SpanFilter{Environment: env}.appendEnvironmentWhere(nil, nil)
		if len(where) != 1 || strings.Contains(where[0], "0 = 1") || len(args) == 0 {
			t.Errorf("environment %q: where %v args %v", env, where, args)
		}
	}
	for _, env := range []string{"", "all"} {
		where, args := SpanFilter{Environment: env}.appendEnvironmentWhere(nil, nil)
		if len(where) != 0 || len(args) != 0 {
			t.Errorf("environment %q adds a condition: %v %v", env, where, args)
		}
	}
}

// The condition ANDs onto a filter expression, and a filter with nested groups
// under or cannot take it.
func TestScopedExpr(t *testing.T) {
	out, err := SpanFilter{Environment: "staging"}.scopedExpr()
	if err != nil || out == nil || len(out.Filters) != 1 {
		t.Fatalf("scoped expr = %+v, %v", out, err)
	}
	leaf := filter.Node{Key: "http.method", Op: filter.OpEq, Value: filter.Scalar("GET")}
	base := &filter.Expr{Match: filter.MatchAnd, Filters: []filter.Node{leaf}}
	out, err = SpanFilter{Environment: "staging", Expr: base}.scopedExpr()
	if err != nil || len(out.Filters) != 2 {
		t.Fatalf("scoped expr with a filter = %+v, %v", out, err)
	}
	nested := &filter.Expr{Match: filter.MatchOr, Filters: []filter.Node{
		{Match: filter.MatchAnd, Filters: []filter.Node{leaf, leaf}}, leaf,
	}}
	if _, err = (SpanFilter{Environment: "staging", Expr: nested}).scopedExpr(); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("nested groups under or: err = %v, want ErrInvalid", err)
	}
}
