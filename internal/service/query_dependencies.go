package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ListDependencies extracts dependency information from client-kind spans.
func (s *QueryService) ListDependencies(ctx context.Context, projectID int64, from, to time.Time, svcFilter string) ([]DependencySummary, error) {
	_, span := tracer.Start(ctx, "query.list_dependencies")
	defer span.End()

	cacheKey := fmt.Sprintf("deps:%d:%s:%d:%d", projectID, svcFilter, from.Truncate(time.Minute).Unix(), to.Truncate(time.Minute).Unix())
	if cached, ok := cache.Get[[]DependencySummary](s.cache, ctx, cacheKey); ok {
		return cached, nil
	}

	sf := repository.SpanFilter{
		ProjectID: projectID,
		Service:   svcFilter,
		From:      from,
		To:        to,
		Limit:     10000,
	}

	spans, err := s.repo.QuerySpans(sf)
	if err != nil {
		return nil, err
	}

	agg := newDependencyAggregator()
	agg.addClientSpans(spans)
	agg.addCrossServiceCalls(spans)
	result := agg.summaries(s.projectSampleRate(ctx, projectID))

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
}

type dependencyKey struct {
	target     string
	targetType string
}

type dependencyStats struct {
	count      int64
	errorCount int64
	durations  []int64
}

// dependencyAggregator collects call statistics per dependency target.
type dependencyAggregator struct {
	byDep map[dependencyKey]*dependencyStats
}

func newDependencyAggregator() *dependencyAggregator {
	return &dependencyAggregator{byDep: make(map[dependencyKey]*dependencyStats)}
}

func (a *dependencyAggregator) add(target, targetType string, sp repository.Span) {
	k := dependencyKey{target, targetType}
	st, ok := a.byDep[k]
	if !ok {
		st = &dependencyStats{}
		a.byDep[k] = st
	}
	st.count++
	if sp.Status == "error" {
		st.errorCount++
	}
	st.durations = append(st.durations, sp.DurationUs)
}

// addClientSpans counts the client spans that name a dependency target.
func (a *dependencyAggregator) addClientSpans(spans []repository.Span) {
	for _, sp := range spans {
		if !isClientKind(sp.Kind) {
			continue
		}
		if target, targetType := extractDependencyTarget(sp.Attributes); target != "" {
			a.add(target, targetType, sp)
		}
	}
}

// addCrossServiceCalls counts a service dependency for every span whose parent
// belongs to another service.
func (a *dependencyAggregator) addCrossServiceCalls(spans []repository.Span) {
	spanByID := make(map[string]*repository.Span, len(spans))
	for i := range spans {
		spanByID[spans[i].SpanID] = &spans[i]
	}
	for _, sp := range spans {
		if sp.ParentSpanID == "" {
			continue
		}
		parent, ok := spanByID[sp.ParentSpanID]
		if !ok {
			continue
		}
		if parent.Service != "" && sp.Service != "" && parent.Service != sp.Service {
			a.add(sp.Service, "service", *parent)
		}
	}
}

// summaries turns the statistics into summaries, inflating counts for the
// project's sample rate and ordering by call count.
func (a *dependencyAggregator) summaries(sampleRate float64) []DependencySummary {
	result := make([]DependencySummary, 0, len(a.byDep))
	for k, st := range a.byDep {
		effective := inflateCount(st.count, st.errorCount, sampleRate)
		var errorRate float64
		if effective > 0 {
			errorRate = float64(st.errorCount) / float64(effective)
		}
		p50, p95, p99 := computePercentiles(st.durations)
		result = append(result, DependencySummary{
			Target:     k.target,
			TargetType: k.targetType,
			CallCount:  effective,
			ErrorCount: st.errorCount,
			ErrorRate:  errorRate,
			P50Us:      p50,
			P95Us:      p95,
			P99Us:      p99,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CallCount > result[j].CallCount
	})
	return result
}

// isClientKind reports whether a span kind is the client kind in either casing.
func isClientKind(kind string) bool {
	return kind == "client" || kind == "CLIENT"
}

// GetDependencyTraces returns recent traces that contain client spans targeting a specific dependency.
func (s *QueryService) GetDependencyTraces(ctx context.Context, projectID int64, target, targetType string, from, to time.Time, limit int) ([]TraceSummary, error) {
	_, span := tracer.Start(ctx, "query.get_dependency_traces")
	defer span.End()

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

	traceIDs := dependencyTraceIDs(spans, target, targetType, limit)

	result := make([]TraceSummary, 0, len(traceIDs))
	for _, tid := range traceIDs {
		traceSpans, err := s.repo.GetTraceByID(tid)
		if err != nil || len(traceSpans) == 0 {
			continue
		}
		result = append(result, summarizeTraceSpans(tid, traceSpans))
	}

	return result, nil
}

// dependencyTraceIDs returns up to limit distinct trace ids that contain a
// client span calling the given dependency, in span order.
func dependencyTraceIDs(spans []repository.Span, target, targetType string, limit int) []string {
	seen := make(map[string]bool)
	var traceIDs []string
	for _, sp := range spans {
		if seen[sp.TraceID] || !isClientKind(sp.Kind) {
			continue
		}
		t, tt := extractDependencyTarget(sp.Attributes)
		if t != target || tt != targetType {
			continue
		}
		seen[sp.TraceID] = true
		traceIDs = append(traceIDs, sp.TraceID)
		if len(traceIDs) >= limit {
			break
		}
	}
	return traceIDs
}

// summarizeTraceSpans builds the list summary of one trace from its spans.
func summarizeTraceSpans(traceID string, traceSpans []repository.Span) TraceSummary {
	root := traceSpans[0]
	for _, ts := range traceSpans {
		if ts.ParentSpanID == "" {
			root = ts
			break
		}
	}
	var totalDur int64
	for _, ts := range traceSpans {
		if ts.DurationUs > totalDur {
			totalDur = ts.DurationUs
		}
	}
	return TraceSummary{
		TraceID:      traceID,
		RootSpanName: root.Name,
		RootService:  root.Service,
		DurationUs:   totalDur,
		SpanCount:    len(traceSpans),
		Status:       root.Status,
		StartTime:    time.UnixMicro(root.StartTimeUs),
	}
}

// dependencyTargetRule maps a span attribute to a dependency target type. A
// hostOnly rule reads the host out of a URL and is skipped when there is none.
type dependencyTargetRule struct {
	key        string
	targetType string
	hostOnly   bool
}

// dependencyTargetRules lists the attributes that name a dependency, most
// specific first.
var dependencyTargetRules = []dependencyTargetRule{
	{"db.system", "database", false},
	{"db.name", "database", false},
	{"peer.service", "service", false},
	{"rpc.service", "rpc", false},
	{"messaging.system", "messaging", false},
	{"aws.service", "aws", false},
	{"http.url", "http", true},
	{"url.full", "http", true},
	{"http.host", "http", false},
	{"server.address", "network", false},
	{"net.peer.name", "network", false},
}

func extractDependencyTarget(attrJSON string) (target, targetType string) {
	if attrJSON == "" || attrJSON == "{}" {
		return "", ""
	}

	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrJSON), &attrs); err != nil {
		return "", ""
	}

	for _, rule := range dependencyTargetRules {
		v, ok := getStringAttr(attrs, rule.key)
		if !ok {
			continue
		}
		if !rule.hostOnly {
			return v, rule.targetType
		}
		if host := extractHost(v); host != "" {
			return host, rule.targetType
		}
	}
	return "", ""
}
