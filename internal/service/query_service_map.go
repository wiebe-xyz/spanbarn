package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func (s *QueryService) GetServiceMap(ctx context.Context, projectID int64, from, to time.Time) (*ServiceMap, error) {
	_, span := tracer.Start(ctx, "query.service_map")
	defer span.End()

	cacheKey := fmt.Sprintf("svcmap:%d:%d:%d", projectID, from.Truncate(time.Minute).Unix(), to.Truncate(time.Minute).Unix())
	if cached, ok := cache.Get[*ServiceMap](s.cache, ctx, cacheKey); ok {
		return cached, nil
	}

	sf := repository.SpanFilter{
		ProjectID: projectID,
		From:      from,
		To:        to,
		Limit:     10000,
	}

	spans, err := s.repo.QuerySpans(sf)
	if err != nil {
		return nil, err
	}

	b := newServiceMapBuilder()
	b.addSpans(spans)
	b.addCrossServiceCalls(spans)
	result := b.build()

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
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

// addSpans counts every span against its service and turns client spans into
// edges towards the dependency they call.
func (b *serviceMapBuilder) addSpans(spans []repository.Span) {
	for _, sp := range spans {
		if sp.Service != "" {
			b.node(sp.Service).record(sp)
		}
		if !isClientKind(sp.Kind) {
			continue
		}
		target, targetType := extractDependencyTarget(sp.Attributes)
		if target == "" || sp.Service == "" {
			continue
		}
		b.addEdge(sp.Service, target, targetType, sp)
		b.node(target) // make sure the dependency exists as a node
	}
}

// addCrossServiceCalls adds an edge for every span whose parent belongs to
// another service.
func (b *serviceMapBuilder) addCrossServiceCalls(spans []repository.Span) {
	spanByID := make(map[string]*repository.Span, len(spans))
	for i := range spans {
		spanByID[spans[i].SpanID] = &spans[i]
	}
	for _, sp := range spans {
		if sp.ParentSpanID == "" {
			continue
		}
		parent, ok := spanByID[sp.ParentSpanID]
		if !ok || parent.Service == "" || sp.Service == "" || parent.Service == sp.Service {
			continue
		}
		b.addEdge(parent.Service, sp.Service, "service", *parent)
	}
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
	sort.Slice(result.Nodes, func(i, j int) bool {
		return result.Nodes[i].SpanCount > result.Nodes[j].SpanCount
	})

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
	sort.Slice(result.Edges, func(i, j int) bool {
		return result.Edges[i].CallCount > result.Edges[j].CallCount
	})
	return result
}
