package service

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// SearchTraces searches for traces matching the given filter.
func (s *QueryService) SearchTraces(ctx context.Context, filter TraceSearchFilter) ([]TraceSummary, error) {
	_, span := tracer.Start(ctx, "query.search_traces")
	span.SetAttributes(
		attribute.String("service", filter.Service),
		attribute.String("operation", filter.Operation),
		attribute.Int("limit", filter.Limit),
	)
	defer span.End()

	sf := repository.SpanFilter{
		ProjectID:   filter.ProjectID,
		Service:     filter.Service,
		Operation:   filter.Operation,
		Status:      filter.Status,
		MinDuration: filter.MinDurationUs,
		From:        filter.From,
		To:          filter.To,
		Limit:       filter.Limit * 5,
		Offset:      0,
	}

	spans, err := s.repo.QuerySpans(sf)
	if err != nil {
		return nil, err
	}

	errorSpans, err := s.repo.QueryErrorSamples(sf)
	if err != nil {
		s.logger.Warn("failed to query error samples", "error", err)
	} else {
		spans = append(spans, errorSpans...)
	}

	type traceInfo struct {
		spans []repository.Span
	}
	byTrace := make(map[string]*traceInfo)
	var traceOrder []string
	for _, sp := range spans {
		ti, ok := byTrace[sp.TraceID]
		if !ok {
			ti = &traceInfo{}
			byTrace[sp.TraceID] = ti
			traceOrder = append(traceOrder, sp.TraceID)
		}
		ti.spans = append(ti.spans, sp)
	}

	var result []TraceSummary
	for _, traceID := range traceOrder {
		ti := byTrace[traceID]

		var root *repository.Span
		for i := range ti.spans {
			if ti.spans[i].ParentSpanID == "" {
				root = &ti.spans[i]
				break
			}
		}
		if root == nil && len(ti.spans) > 0 {
			root = &ti.spans[0]
		}
		if root == nil {
			continue
		}

		status := "ok"
		for _, sp := range ti.spans {
			if sp.Status == "error" {
				status = "error"
				break
			}
		}

		seenSpans := make(map[string]bool)
		for _, sp := range ti.spans {
			seenSpans[sp.SpanID] = true
		}

		spanCount := len(seenSpans)
		if filter.MinSpans > 0 && spanCount < filter.MinSpans {
			continue
		}

		result = append(result, TraceSummary{
			TraceID:      traceID,
			RootSpanName: root.Name,
			RootService:  root.Service,
			DurationUs:   root.DurationUs,
			SpanCount:    spanCount,
			Status:       status,
			StartTime:    time.UnixMicro(root.StartTimeUs),
		})
	}

	if filter.Offset > 0 && filter.Offset < len(result) {
		result = result[filter.Offset:]
	} else if filter.Offset >= len(result) {
		return []TraceSummary{}, nil
	}

	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}

	return result, nil
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

	return &TraceDetail{
		TraceID:    traceID,
		Spans:      spans,
		DurationUs: root.DurationUs,
		Service:    root.Service,
		Name:       root.Name,
	}, nil
}
