package service

import (
	"context"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// SearchTraces searches for traces matching the given filter.
//
// Grouping happens in SQL via SearchTraceSummaries — the previous
// implementation fetched up to filter.Limit*5 raw spans and grouped them in
// Go, which dominated /traces page latency on busy projects.
func (s *QueryService) SearchTraces(ctx context.Context, filter TraceSearchFilter) ([]TraceSummary, error) {
	_, span := tracer.Start(ctx, "query.search_traces")
	span.SetAttributes(
		attribute.String("service", filter.Service),
		attribute.String("operation", filter.Operation),
		attribute.Int("limit", filter.Limit),
	)
	defer span.End()

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	// Merge caller-supplied exclusions with project-level saved exclusions.
	excluded := filter.ExcludeOperations
	if filter.ProjectID != 0 {
		if saved, err := s.repo.ExcludedOperations(filter.ProjectID); err == nil && len(saved) > 0 {
			seen := make(map[string]bool, len(excluded))
			for _, op := range excluded {
				seen[op] = true
			}
			for _, op := range saved {
				if !seen[op] {
					excluded = append(excluded, op)
				}
			}
		}
	}

	sf := repository.SpanFilter{
		ProjectID:         filter.ProjectID,
		Service:           filter.Service,
		Operation:         filter.Operation,
		Status:            filter.Status,
		MinDuration:       filter.MinDurationUs,
		RootOnly:          filter.RootOnly,
		SortErrorsFirst:   filter.SortErrorsFirst,
		ExcludeOperations: excluded,
		From:              filter.From,
		To:                filter.To,
		Limit:             limit,
		Offset:            filter.Offset,
	}

	rows, err := s.repo.SearchTraceSummaries(sf, filter.MinSpans)
	if err != nil {
		return nil, err
	}

	result := make([]TraceSummary, 0, len(rows))
	for _, r := range rows {
		status := "ok"
		if r.HasError {
			status = "error"
		}
		result = append(result, TraceSummary{
			TraceID:      r.TraceID,
			RootSpanName: r.RootName,
			RootService:  r.RootService,
			DurationUs:   r.RootDuration,
			SpanCount:    r.SpanCount,
			Status:       status,
			StartTime:    time.UnixMicro(r.StartTimeUs),
		})
	}
	return result, nil
}

// ListTraceGroups returns per-operation aggregates over root spans for the grouped traces view.
// Results are sorted by error count descending, then by total count descending.
func (s *QueryService) ListTraceGroups(ctx context.Context, filter TraceSearchFilter) ([]TraceGroupSummary, error) {
	_, span := tracer.Start(ctx, "query.list_trace_groups")
	span.SetAttributes(
		attribute.String("service", filter.Service),
		attribute.String("status", filter.Status),
	)
	defer span.End()

	// Merge saved exclusions with caller-supplied ones.
	excluded := filter.ExcludeOperations
	if filter.ProjectID != 0 {
		if saved, err := s.repo.ExcludedOperations(filter.ProjectID); err == nil && len(saved) > 0 {
			seen := make(map[string]bool, len(excluded))
			for _, op := range excluded {
				seen[op] = true
			}
			for _, op := range saved {
				if !seen[op] {
					excluded = append(excluded, op)
				}
			}
		}
	}

	sf := repository.SpanFilter{
		ProjectID:         filter.ProjectID,
		Service:           filter.Service,
		Status:            filter.Status,
		MinDuration:       filter.MinDurationUs,
		ExcludeOperations: excluded,
		From:              filter.From,
		To:                filter.To,
	}

	groups, err := s.repo.QueryRootSpanGroups(ctx, sf)
	if err != nil {
		return nil, err
	}

	sr := s.projectSampleRate(ctx, filter.ProjectID)
	out := make([]TraceGroupSummary, 0, len(groups))
	for _, g := range groups {
		p50, p95, p99 := computePercentiles(g.Durations)
		effective := inflateCount(g.Count, g.ErrorCount, sr)
		errorRate := 0.0
		if effective > 0 {
			errorRate = float64(g.ErrorCount) / float64(effective)
		}
		out = append(out, TraceGroupSummary{
			Operation:  g.Operation,
			Service:    g.Service,
			Count:      effective,
			ErrorCount: g.ErrorCount,
			ErrorRate:  errorRate,
			P50Us:      p50,
			P95Us:      p95,
			P99Us:      p99,
		})
	}

	// Sort: errors first, then by count desc.
	sort.Slice(out, func(i, j int) bool {
		if out[i].ErrorCount != out[j].ErrorCount {
			return out[i].ErrorCount > out[j].ErrorCount
		}
		return out[i].Count > out[j].Count
	})

	return out, nil
}

// GetTrace returns full trace detail for a given trace ID.
func (s *QueryService) GetTrace(ctx context.Context, traceID string) (*TraceDetail, error) {
	_, span := tracer.Start(ctx, "query.get_trace")
	span.SetAttributes(attribute.String("trace_id", traceID))
	defer span.End()

	spans, err := s.repo.GetTraceByID(traceID)
	if err != nil {
		return nil, err
	}

	errorSpans, err := s.repo.QueryErrorSamples(repository.SpanFilter{
		TraceID: traceID,
		Limit:   1000,
	})
	if err != nil {
		s.logger.Warn("failed to query error samples for trace", "error", err)
	} else {
		spans = append(spans, errorSpans...)
	}

	seen := make(map[string]bool)
	var unique []repository.Span
	for _, sp := range spans {
		if !seen[sp.SpanID] {
			seen[sp.SpanID] = true
			unique = append(unique, sp)
		}
	}
	spans = unique

	if len(spans) == 0 {
		return nil, nil
	}

	var root *repository.Span
	for i := range spans {
		if spans[i].ParentSpanID == "" {
			root = &spans[i]
			break
		}
	}
	if root == nil {
		root = &spans[0]
	}

	totalSpans := len(spans)
	truncated := false
	if totalSpans > MaxTraceDetailSpans {
		// Always keep the root in the truncated view; otherwise the UI loses
		// the trace's identity. Stable order is start_time_us (set by repo).
		rootID := root.SpanID
		spans = spans[:MaxTraceDetailSpans]
		hasRoot := false
		for i := range spans {
			if spans[i].SpanID == rootID {
				hasRoot = true
				break
			}
		}
		if !hasRoot {
			spans[0] = *root
		}
		truncated = true
	}

	return &TraceDetail{
		TraceID:    traceID,
		Spans:      spans,
		DurationUs: root.DurationUs,
		Service:    root.Service,
		Name:       root.Name,
		TotalSpans: totalSpans,
		Truncated:  truncated,
	}, nil
}
