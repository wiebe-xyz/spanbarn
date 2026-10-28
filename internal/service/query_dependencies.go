package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/deptarget"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ListDependencies lists the targets the project's services call. A range of
// an hour or more reads span_edges where they hold it. Otherwise it reads the
// range's newest spanReadLimit spans; Coverage says when that was less than
// the whole range.
func (s *QueryService) ListDependencies(ctx context.Context, projectID int64, from, to time.Time, svcFilter string) ([]DependencySummary, Coverage, error) {
	_, span := tracer.Start(ctx, "query.list_dependencies")
	defer span.End()

	from, to, grain := snapRange(from, to)
	cacheKey := fmt.Sprintf("deps:%d:%s:%d:%d", projectID, svcFilter, from.Unix(), to.Unix())
	list, err := readCached(ctx, s.cache, cacheKey, grain, func(ctx context.Context) (coveredList[DependencySummary], error) {
		if deps, ok, err := s.dependenciesFromRollups(ctx, projectID, from, to, svcFilter); ok {
			return coveredList[DependencySummary]{items: deps}, err
		}

		sf := repository.SpanFilter{
			ProjectID: projectID,
			Service:   svcFilter,
			From:      from,
			To:        to,
			Limit:     spanReadLimit,
		}

		spans, err := s.repo.QuerySpans(sf)
		if err != nil {
			return coveredList[DependencySummary]{}, err
		}

		agg := newDependencyAggregator()
		agg.addCalls(spans)
		result := agg.summaries(s.projectSampleRate(ctx, projectID))

		return coveredList[DependencySummary]{items: result, coverage: spanCoverage(spans)}, nil
	})
	return list.items, list.coverage, err
}

type dependencyKey struct {
	target     string
	targetType string
}

type dependencyStats struct {
	count      int64
	errorCount int64
	weights    repository.SpanWeights
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
	st.weights.Add(sp.SampleWeight)
	if sp.Status == "error" {
		st.errorCount++
	}
	st.durations = append(st.durations, sp.DurationUs)
}

// addCalls counts every call the spans show towards its target.
func (a *dependencyAggregator) addCalls(spans []repository.Span) {
	deptarget.Calls(spanView(spans), func(c deptarget.Call) {
		a.add(c.Target, c.TargetType, spans[c.Span])
	})
}

// summaries turns the statistics into summaries, estimating each call count
// from the spans' sample weights (or the project's sample rate for unweighted
// spans) and ordering by call count.
func (a *dependencyAggregator) summaries(sampleRate float64) []DependencySummary {
	result := make([]DependencySummary, 0, len(a.byDep))
	for k, st := range a.byDep {
		effective := estimateSpans(st.count, st.errorCount, st.weights, sampleRate)
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
			Source:     SourceSpans,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CallCount > result[j].CallCount
	})
	return result
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
		if seen[sp.TraceID] || !deptarget.IsClientKind(sp.Kind) {
			continue
		}
		t, tt := deptarget.Extract(sp.Attributes)
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

// spanView lets deptarget walk stored spans.
type spanView []repository.Span

func (v spanView) Len() int { return len(v) }

func (v spanView) At(i int) deptarget.Span {
	sp := &v[i]
	return deptarget.Span{TraceID: sp.TraceID, SpanID: sp.SpanID, ParentSpanID: sp.ParentSpanID,
		Service: sp.Service, Kind: sp.Kind, Attributes: sp.Attributes}
}
