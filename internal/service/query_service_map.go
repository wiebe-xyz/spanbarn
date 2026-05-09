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

	type nodeStats struct {
		count, errorCount int64
	}
	nodes := make(map[string]*nodeStats)

	addNode := func(name string, sp repository.Span) {
		n, ok := nodes[name]
		if !ok {
			n = &nodeStats{}
			nodes[name] = n
		}
		n.count++
		if sp.Status == "error" {
			n.errorCount++
		}
	}

	type edgeKey struct{ source, target, targetType string }
	type edgeStats struct {
		count, errorCount int64
	}
	edges := make(map[edgeKey]*edgeStats)

	addEdge := func(source, target, targetType string, sp repository.Span) {
		k := edgeKey{source, target, targetType}
		e, ok := edges[k]
		if !ok {
			e = &edgeStats{}
			edges[k] = e
		}
		e.count++
		if sp.Status == "error" {
			e.errorCount++
		}
	}

	for _, sp := range spans {
		if sp.Service != "" {
			addNode(sp.Service, sp)
		}

		if sp.Kind == "client" || sp.Kind == "CLIENT" {
			target, targetType := extractDependencyTarget(sp.Attributes)
			if target != "" && sp.Service != "" {
				addEdge(sp.Service, target, targetType, sp)
				if _, ok := nodes[target]; !ok {
					nodes[target] = &nodeStats{}
				}
			}
		}
	}

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
		addEdge(parent.Service, sp.Service, "service", *parent)
	}

	result := &ServiceMap{}

	for name, st := range nodes {
		var errorRate float64
		if st.count > 0 {
			errorRate = float64(st.errorCount) / float64(st.count)
		}
		result.Nodes = append(result.Nodes, ServiceMapNode{
			ID:         name,
			SpanCount:  st.count,
			ErrorCount: st.errorCount,
			ErrorRate:  errorRate,
		})
	}
	sort.Slice(result.Nodes, func(i, j int) bool {
		return result.Nodes[i].SpanCount > result.Nodes[j].SpanCount
	})

	for k, st := range edges {
		var errorRate float64
		if st.count > 0 {
			errorRate = float64(st.errorCount) / float64(st.count)
		}
		result.Edges = append(result.Edges, ServiceMapEdge{
			Source:     k.source,
			Target:     k.target,
			TargetType: k.targetType,
			CallCount:  st.count,
			ErrorCount: st.errorCount,
			ErrorRate:  errorRate,
		})
	}
	sort.Slice(result.Edges, func(i, j int) bool {
		return result.Edges[i].CallCount > result.Edges[j].CallCount
	})

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
}
