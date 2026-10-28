package service

import (
	"context"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// foldSpanEdges folds the call rows q selects into one summary per key. Each
// row is estimated from its sample weights, or the project's sample rate, as
// the raw dependency read estimates its spans.
func foldSpanEdges[K comparable](ctx context.Context, s *QueryService, q repository.SpanEdgeScan, key func(repository.SpanEdge) (K, bool)) (map[K]*rollupFold, error) {
	rate := s.projectSampleRate(ctx, q.ProjectID)
	out := map[K]*rollupFold{}
	err := s.repo.ScanSpanEdges(ctx, q, func(e repository.SpanEdge) error {
		k, ok := key(e)
		if !ok {
			return nil
		}
		f := out[k]
		if f == nil {
			f = &rollupFold{}
			out[k] = f
		}
		f.addCounts(e.Count, e.ErrorCount, repository.SpanWeights{Count: e.WeightedCount, Spans: e.WeightedSpans}, e.Hist, rate)
		return nil
	})
	return out, err
}

// edgeRollupWindow reports whether a range is wide enough to read from the
// projections: they read whole 5-minute buckets at both edges.
func edgeRollupWindow(from, to time.Time) bool {
	return !from.IsZero() && !to.IsZero() && to.Sub(from) >= traceGroupRollupMinWindow
}

// serviceMapFromRollups builds the service map from span_rollups (nodes) and
// span_edges (edges). ok is false when the range is under an hour or either
// table does not hold all of it.
func (s *QueryService) serviceMapFromRollups(ctx context.Context, projectID int64, from, to time.Time) (*ServiceMap, bool, error) {
	if !edgeRollupWindow(from, to) {
		return nil, false, nil
	}
	nodeSegs, ok := s.spanRollupPlan(ctx, from, to, 0)
	if !ok {
		return nil, false, nil
	}
	edgeSegs, ok := s.spanEdgePlan(ctx, from, to, 0)
	if !ok {
		return nil, false, nil
	}
	nodes, err := foldSpanRollups(ctx, s, projectID, repository.SpanRollupScan{Segments: nodeSegs, ProjectID: projectID},
		func(r repository.SpanRollup) string { return r.Service })
	if err != nil {
		return nil, true, err
	}
	edges, err := foldSpanEdges(ctx, s, repository.SpanEdgeScan{Segments: edgeSegs, ProjectID: projectID},
		func(e repository.SpanEdge) (serviceMapEdgeKey, bool) {
			return serviceMapEdgeKey{e.Source, e.Target, e.TargetType}, e.Source != ""
		})
	if err != nil {
		return nil, true, err
	}
	return buildRollupServiceMap(nodes, edges), true, nil
}

// buildRollupServiceMap turns the folds into a map. Every edge end is a node,
// so a dependency that is not a service still shows.
func buildRollupServiceMap(nodes map[string]*rollupFold, edges map[serviceMapEdgeKey]*rollupFold) *ServiceMap {
	m := &ServiceMap{Source: SourceRollups}
	seen := map[string]bool{"": true}
	for svc, f := range nodes {
		if seen[svc] {
			continue
		}
		seen[svc] = true
		m.Nodes = append(m.Nodes, ServiceMapNode{ID: svc, SpanCount: f.effective, ErrorCount: f.errorCount, ErrorRate: f.errorRate()})
	}
	for k, f := range edges {
		m.Edges = append(m.Edges, ServiceMapEdge{
			Source: k.source, Target: k.target, TargetType: k.targetType,
			CallCount: f.effective, ErrorCount: f.errorCount, ErrorRate: f.errorRate(),
		})
		for _, id := range []string{k.source, k.target} {
			if !seen[id] {
				seen[id] = true
				m.Nodes = append(m.Nodes, ServiceMapNode{ID: id})
			}
		}
	}
	sortServiceMap(m)
	return m
}

// dependenciesFromRollups lists the call targets from span_edges, of every
// service or of svc. ok is false when the range is under an hour or the
// edges do not hold all of it.
func (s *QueryService) dependenciesFromRollups(ctx context.Context, projectID int64, from, to time.Time, svc string) ([]DependencySummary, bool, error) {
	if !edgeRollupWindow(from, to) {
		return nil, false, nil
	}
	segs, ok := s.spanEdgePlan(ctx, from, to, 0)
	if !ok {
		return nil, false, nil
	}
	byDep, err := foldSpanEdges(ctx, s, repository.SpanEdgeScan{Segments: segs, ProjectID: projectID, Source: svc},
		func(e repository.SpanEdge) (dependencyKey, bool) { return dependencyKey{e.Target, e.TargetType}, true })
	if err != nil {
		return nil, true, err
	}
	out := make([]DependencySummary, 0, len(byDep))
	for k, f := range byDep {
		out = append(out, DependencySummary{
			Target: k.target, TargetType: k.targetType,
			CallCount: f.effective, ErrorCount: f.errorCount, ErrorRate: f.errorRate(),
			P50Us: f.hist.Percentile(50), P95Us: f.hist.Percentile(95), P99Us: f.hist.Percentile(99),
			Source: SourceRollups,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CallCount != out[j].CallCount {
			return out[i].CallCount > out[j].CallCount
		}
		return out[i].Target < out[j].Target
	})
	return out, true, nil
}
