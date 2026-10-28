package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// edgeFixture is rollupFixture with client spans that name a database or an
// HTTP host, some of them failing, next to the cross-service calls from web
// and api into db.
func edgeFixture() []repository.Span {
	spans := rollupFixture()
	for i := range spans {
		switch {
		case spans[i].Kind != "client":
		case i%4 == 1:
			spans[i].Attributes = `{"db.system":"sqlite"}`
		case i%8 == 3:
			spans[i].Attributes, spans[i].Status = `{"http.url":"https://pay.example.com/x"}`, "error"
		}
	}
	return spans
}

func edgeServices(t *testing.T) (rollups, spans *QueryService, repo *repository.Repository) {
	t.Helper()
	repo = setupTestRepo(t)
	seedRollupFixture(t, repo, edgeFixture())
	rollups = NewQueryService(repo, nil, nil)
	rollups.SetSpanRollupReads(SpanRollupReads{Enabled: true, HTTPStatus: true})
	return rollups, NewQueryService(repo, nil, nil), repo
}

// closeTo reports whether an approximate percentile is within a quarter
// octave of the exact one, the histogram's resolution.
func closeTo(got, exact int64) bool {
	return float64(got) <= float64(exact)*1.19+1 && float64(got) >= float64(exact)/1.19-1
}

func depsByTarget(deps []DependencySummary) map[dependencyKey]DependencySummary {
	out := map[dependencyKey]DependencySummary{}
	for _, d := range deps {
		out[dependencyKey{d.Target, d.TargetType}] = d
	}
	return out
}

// Over unweighted spans that were all stored, span_edges list the same
// dependencies as the raw spans. With a service filter the raw read sees no
// cross-service call, because the filter drops the other service's spans;
// span_edges keep the calls of the filtered service.
func TestDependenciesFromEdgesMatchRawSpans(t *testing.T) {
	rollups, spans, _ := edgeServices(t)
	ctx := context.Background()
	to := rollupBase.Add(2 * time.Hour)
	for _, svc := range []string{"", "db"} {
		got, _, err := rollups.ListDependencies(ctx, 1, rollupBase, to, svc)
		if err != nil {
			t.Fatal(err)
		}
		want, _, err := spans.ListDependencies(ctx, 1, rollupBase, to, svc)
		if err != nil {
			t.Fatal(err)
		}
		if len(want) < 2 || len(got) != len(want) {
			t.Fatalf("service %q: rollups %+v\nspans %+v", svc, got, want)
		}
		byTarget := depsByTarget(want)
		for _, g := range got {
			w := byTarget[dependencyKey{g.Target, g.TargetType}]
			wantSource(t, "dependencies", g.Source, SourceRollups)
			wantSource(t, "raw dependencies", w.Source, SourceSpans)
			if g.CallCount != w.CallCount || g.ErrorCount != w.ErrorCount || g.ErrorRate != w.ErrorRate ||
				!closeTo(g.P50Us, w.P50Us) || !closeTo(g.P99Us, w.P99Us) {
				t.Errorf("service %q %s/%s: rollups %+v, spans %+v", svc, g.TargetType, g.Target, g, w)
			}
		}
	}

	web, _, err := rollups.ListDependencies(ctx, 1, rollupBase, to, "web")
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := depsByTarget(web)[dependencyKey{"db", "service"}]; !ok || d.CallCount != 24 {
		t.Errorf("web's calls into db = %+v, want 24", web)
	}
}

func TestServiceMapFromEdgesMatchesRawSpans(t *testing.T) {
	rollups, spans, _ := edgeServices(t)
	ctx := context.Background()
	to := rollupBase.Add(2 * time.Hour)
	got, err := rollups.GetServiceMap(ctx, 1, rollupBase, to)
	if err != nil {
		t.Fatal(err)
	}
	want, err := spans.GetServiceMap(ctx, 1, rollupBase, to)
	if err != nil {
		t.Fatal(err)
	}
	wantSource(t, "service map", got.Source, SourceRollups)
	wantSource(t, "raw service map", want.Source, SourceSpans)

	nodes := map[string]ServiceMapNode{}
	for _, n := range want.Nodes {
		nodes[n.ID] = n
	}
	if len(got.Nodes) != len(want.Nodes) {
		t.Fatalf("nodes: rollups %+v\nspans %+v", got.Nodes, want.Nodes)
	}
	for _, n := range got.Nodes {
		if n != nodes[n.ID] {
			t.Errorf("node %s: rollups %+v, spans %+v", n.ID, n, nodes[n.ID])
		}
	}
	edges := map[serviceMapEdgeKey]ServiceMapEdge{}
	for _, e := range want.Edges {
		edges[serviceMapEdgeKey{e.Source, e.Target, e.TargetType}] = e
	}
	if len(want.Edges) < 4 || len(got.Edges) != len(want.Edges) {
		t.Fatalf("edges: rollups %+v\nspans %+v", got.Edges, want.Edges)
	}
	for _, e := range got.Edges {
		if w := edges[serviceMapEdgeKey{e.Source, e.Target, e.TargetType}]; e != w {
			t.Errorf("edge %s->%s: rollups %+v, spans %+v", e.Source, e.Target, e, w)
		}
	}
}

// A window under an hour, a window before span_edges start, and a 5-minute
// bucket without edge rows all read the raw spans.
func TestEdgeReadsSourceSelection(t *testing.T) {
	rollups, _, repo := edgeServices(t)
	ctx := context.Background()
	to := rollupBase.Add(2 * time.Hour)
	if _, ok, _ := rollups.dependenciesFromRollups(ctx, 1, to.Add(-30*time.Minute), to, ""); ok {
		t.Error("dependencies read span_edges for a 30-minute window")
	}
	if _, ok, _ := rollups.serviceMapFromRollups(ctx, 1, to.Add(-30*time.Minute), to); ok {
		t.Error("service map read span_edges for a 30-minute window")
	}

	// span_edges start 30 minutes after span_rollups, as after a deploy.
	if _, err := repo.DB().Exec(`DELETE FROM span_edges WHERE bucket < ?`, rollupBase.Add(30*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	later := NewQueryService(repo, nil, nil)
	later.SetSpanRollupReads(SpanRollupReads{Enabled: true})
	if _, ok, _ := later.dependenciesFromRollups(ctx, 1, rollupBase, to, ""); ok {
		t.Error("dependencies read span_edges before their oldest bucket")
	}
	m, err := later.GetServiceMap(ctx, 1, rollupBase, to)
	if err != nil {
		t.Fatal(err)
	}
	wantSource(t, "service map before span_edges start", m.Source, SourceSpans)
	if _, ok, _ := later.servicesFromRollups(ctx, 1, rollupBase, to, ""); !ok {
		t.Error("services stopped reading span_rollups")
	}
	deps, _, err := later.ListDependencies(ctx, 1, rollupBase.Add(30*time.Minute), to, "")
	if err != nil || len(deps) == 0 {
		t.Fatalf("dependencies: %v, %v", deps, err)
	}
	wantSource(t, "dependencies inside span_edges", deps[0].Source, SourceRollups)

	// A bucket in the middle without a marker is a gap.
	if _, err := repo.DB().Exec(`DELETE FROM span_edges WHERE step_seconds = 300 AND bucket = ?`,
		rollupBase.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	gapped := NewQueryService(repo, nil, nil)
	gapped.SetSpanRollupReads(SpanRollupReads{Enabled: true})
	if _, ok, _ := gapped.dependenciesFromRollups(ctx, 1, rollupBase.Add(30*time.Minute), to, ""); ok {
		t.Error("dependencies read span_edges across a gap")
	}
}
