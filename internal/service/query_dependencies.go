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

	type depKey struct {
		target     string
		targetType string
	}
	type depStats struct {
		count      int64
		errorCount int64
		durations  []int64
	}
	byDep := make(map[depKey]*depStats)

	addDep := func(target, targetType string, sp repository.Span) {
		k := depKey{target, targetType}
		st, ok := byDep[k]
		if !ok {
			st = &depStats{}
			byDep[k] = st
		}
		st.count++
		if sp.Status == "error" {
			st.errorCount++
		}
		st.durations = append(st.durations, sp.DurationUs)
	}

	for _, sp := range spans {
		if sp.Kind == "client" || sp.Kind == "CLIENT" {
			target, targetType := extractDependencyTarget(sp.Attributes)
			if target != "" {
				addDep(target, targetType, sp)
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
		if !ok {
			continue
		}
		if parent.Service != "" && sp.Service != "" && parent.Service != sp.Service {
			addDep(sp.Service, "service", *parent)
		}
	}

	sr := s.projectSampleRate(ctx, projectID)
	result := make([]DependencySummary, 0, len(byDep))
	for k, st := range byDep {
		effective := inflateCount(st.count, st.errorCount, sr)
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

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
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

	seen := make(map[string]bool)
	var traceIDs []string
	for _, sp := range spans {
		if seen[sp.TraceID] {
			continue
		}
		if sp.Kind != "client" && sp.Kind != "CLIENT" {
			continue
		}
		t, tt := extractDependencyTarget(sp.Attributes)
		if t == target && tt == targetType {
			seen[sp.TraceID] = true
			traceIDs = append(traceIDs, sp.TraceID)
			if len(traceIDs) >= limit {
				break
			}
		}
	}

	result := make([]TraceSummary, 0, len(traceIDs))
	for _, tid := range traceIDs {
		traceSpans, err := s.repo.GetTraceByID(tid)
		if err != nil || len(traceSpans) == 0 {
			continue
		}
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
		result = append(result, TraceSummary{
			TraceID:      tid,
			RootSpanName: root.Name,
			RootService:  root.Service,
			DurationUs:   totalDur,
			SpanCount:    len(traceSpans),
			Status:       root.Status,
			StartTime:    time.UnixMicro(root.StartTimeUs),
		})
	}

	return result, nil
}

func extractDependencyTarget(attrJSON string) (target, targetType string) {
	if attrJSON == "" || attrJSON == "{}" {
		return "", ""
	}

	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrJSON), &attrs); err != nil {
		return "", ""
	}

	if v, ok := getStringAttr(attrs, "db.system"); ok {
		return v, "database"
	}
	if v, ok := getStringAttr(attrs, "db.name"); ok {
		return v, "database"
	}
	if v, ok := getStringAttr(attrs, "peer.service"); ok {
		return v, "service"
	}
	if v, ok := getStringAttr(attrs, "rpc.service"); ok {
		return v, "rpc"
	}
	if v, ok := getStringAttr(attrs, "messaging.system"); ok {
		return v, "messaging"
	}
	if v, ok := getStringAttr(attrs, "aws.service"); ok {
		return v, "aws"
	}
	if v, ok := getStringAttr(attrs, "http.url"); ok {
		if host := extractHost(v); host != "" {
			return host, "http"
		}
	}
	if v, ok := getStringAttr(attrs, "url.full"); ok {
		if host := extractHost(v); host != "" {
			return host, "http"
		}
	}
	if v, ok := getStringAttr(attrs, "http.host"); ok {
		return v, "http"
	}
	if v, ok := getStringAttr(attrs, "server.address"); ok {
		return v, "network"
	}
	if v, ok := getStringAttr(attrs, "net.peer.name"); ok {
		return v, "network"
	}

	return "", ""
}
