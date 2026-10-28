package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/deptarget"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func (s *QueryService) GetServiceMap(ctx context.Context, projectID int64, from, to time.Time) (*ServiceMap, error) {
	_, span := tracer.Start(ctx, "query.service_map")
	defer span.End()

	from, to, grain := snapRange(from, to)
	cacheKey := fmt.Sprintf("svcmap:%d:%d:%d", projectID, from.Unix(), to.Unix())
	return readCached(ctx, s.cache, cacheKey, grain, func(ctx context.Context) (*ServiceMap, error) {
		if m, ok, err := s.serviceMapFromRollups(ctx, projectID, from, to); ok {
			return m, err
		}

		sf := repository.SpanFilter{
			ProjectID: projectID,
			From:      from,
			To:        to,
			Limit:     spanReadLimit,
		}

		spans, err := s.repo.QuerySpans(sf)
		if err != nil {
			return nil, err
		}

		b := newServiceMapBuilder()
		b.addSpans(spans)
		result := b.build()
		result.Coverage = spanCoverage(spans)

		return result, nil
	})
}

// callStats counts spans and the errors among them.
type callStats struct {
	count, errorCount int64
}

func (c *callStats) record(sp repository.Span) {
	c.count++
	if sp.Status == "error" {
		c.errorCount++
	}
}

// errorRate is the share of errors, or 0 without any call.
func (c *callStats) errorRate() float64 {
	if c.count == 0 {
		return 0
	}
	return float64(c.errorCount) / float64(c.count)
}

type serviceMapEdgeKey struct{ source, target, targetType string }

// serviceMapBuilder accumulates the nodes and edges of a service map.
type serviceMapBuilder struct {
	nodes map[string]*callStats
	edges map[serviceMapEdgeKey]*callStats
}

func newServiceMapBuilder() *serviceMapBuilder {
	return &serviceMapBuilder{
		nodes: make(map[string]*callStats),
		edges: make(map[serviceMapEdgeKey]*callStats),
	}
}

func (b *serviceMapBuilder) node(name string) *callStats {
	n, ok := b.nodes[name]
	if !ok {
		n = &callStats{}
		b.nodes[name] = n
	}
	return n
}

func (b *serviceMapBuilder) addEdge(source, target, targetType string, sp repository.Span) {
	k := serviceMapEdgeKey{source, target, targetType}
	e, ok := b.edges[k]
	if !ok {
		e = &callStats{}
		b.edges[k] = e
	}
	e.record(sp)
}

// addSpans counts every span against its service and adds an edge for every
// call the spans show. A dependency that is not a service still gets a node.
func (b *serviceMapBuilder) addSpans(spans []repository.Span) {
	for _, sp := range spans {
		if sp.Service != "" {
			b.node(sp.Service).record(sp)
		}
	}
	deptarget.Calls(spanView(spans), func(c deptarget.Call) {
		if c.Source == "" {
			return
		}
		b.addEdge(c.Source, c.Target, c.TargetType, spans[c.Span])
		b.node(c.Target)
	})
}

// build returns the map with nodes and edges ordered by volume.
func (b *serviceMapBuilder) build() *ServiceMap {
	result := &ServiceMap{}

	for name, st := range b.nodes {
		result.Nodes = append(result.Nodes, ServiceMapNode{
			ID:         name,
			SpanCount:  st.count,
			ErrorCount: st.errorCount,
			ErrorRate:  st.errorRate(),
		})
	}
	for k, st := range b.edges {
		result.Edges = append(result.Edges, ServiceMapEdge{
			Source:     k.source,
			Target:     k.target,
			TargetType: k.targetType,
			CallCount:  st.count,
			ErrorCount: st.errorCount,
			ErrorRate:  st.errorRate(),
		})
	}
	sortServiceMap(result)
	result.Source = SourceSpans
	return result
}

// sortServiceMap orders nodes and edges by volume.
func sortServiceMap(m *ServiceMap) {
	sort.Slice(m.Nodes, func(i, j int) bool {
		return m.Nodes[i].SpanCount > m.Nodes[j].SpanCount
	})
	sort.Slice(m.Edges, func(i, j int) bool {
		return m.Edges[i].CallCount > m.Edges[j].CallCount
	})
}
