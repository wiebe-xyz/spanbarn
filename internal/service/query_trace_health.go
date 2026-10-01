package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ErrInvalidTraceHealthRequest marks a trace health request the caller must fix
// (missing project, missing or oversized range). The API maps it to HTTP 400.
var ErrInvalidTraceHealthRequest = errors.New("invalid trace health request")

// MaxTraceHealthWindow is the widest range a trace health view serves. The
// orphan and single-span checks join spans back to themselves, so the window is
// what bounds their cost on a multi-GB database.
const MaxTraceHealthWindow = 7 * 24 * time.Hour

const (
	defaultTraceHealthLimit = 100
	maxTraceHealthLimit     = 500
)

// TraceHealthQuery scopes every trace health view. ProjectID, From and To are
// required.
type TraceHealthQuery struct {
	ProjectID int64
	From, To  time.Time
	Limit     int
}

// OrphanSpanGroup is a set of spans whose parent was never ingested, sharing
// name, kind and service. SampleTraceID links to one example.
type OrphanSpanGroup struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Service       string `json:"service"`
	Count         int64  `json:"count"`
	SampleTraceID string `json:"sampleTraceId"`
}

// SingleSpanTraceGroup is a set of traces with exactly one span, sharing that
// span's name and service.
type SingleSpanTraceGroup struct {
	Name          string `json:"name"`
	Service       string `json:"service"`
	Count         int64  `json:"count"`
	SampleTraceID string `json:"sampleTraceId"`
}

// SpanNameSummary is the stored count of one span name and how many of those
// spans are roots. Counts are stored spans, not sample-corrected estimates.
type SpanNameSummary struct {
	Name      string `json:"name"`
	Count     int64  `json:"count"`
	RootCount int64  `json:"rootCount"`
}

// RootlessTraces lists traces that hold no root span.
type RootlessTraces struct {
	// Total is every rootless trace in the window; Traces is the newest Limit.
	Total  int64          `json:"total"`
	Traces []TraceSummary `json:"traces"`
}

func (q TraceHealthQuery) window() (repository.HealthWindow, error) {
	switch {
	case q.ProjectID == 0:
		return repository.HealthWindow{}, fmt.Errorf("%w: project_id is required", ErrInvalidTraceHealthRequest)
	case q.From.IsZero() || q.To.IsZero():
		return repository.HealthWindow{}, fmt.Errorf("%w: from and to are required", ErrInvalidTraceHealthRequest)
	case !q.To.After(q.From):
		return repository.HealthWindow{}, fmt.Errorf("%w: to must be after from", ErrInvalidTraceHealthRequest)
	case q.To.Sub(q.From) > MaxTraceHealthWindow:
		return repository.HealthWindow{}, fmt.Errorf("%w: range is limited to %s", ErrInvalidTraceHealthRequest, MaxTraceHealthWindow)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultTraceHealthLimit
	}
	if limit > maxTraceHealthLimit {
		limit = maxTraceHealthLimit
	}
	return repository.HealthWindow{ProjectID: q.ProjectID, From: q.From.UTC(), To: q.To.UTC(), Limit: limit}, nil
}

// ListOrphanSpans groups the spans whose parent_span_id has no matching span in
// the same trace.
func (s *QueryService) ListOrphanSpans(ctx context.Context, q TraceHealthQuery) ([]OrphanSpanGroup, error) {
	return healthView(ctx, q, "orphan_spans", s.repo.QueryOrphanSpanGroups,
		func(r repository.OrphanSpanGroup) OrphanSpanGroup { return OrphanSpanGroup(r) })
}

// healthView validates the query, opens a span and maps the repository rows to
// their service type.
func healthView[R, O any](ctx context.Context, q TraceHealthQuery, name string,
	fetch func(context.Context, repository.HealthWindow) ([]R, error), conv func(R) O) ([]O, error) {
	w, err := q.window()
	if err != nil {
		return nil, err
	}
	ctx, span := tracer.Start(ctx, "query.trace_health."+name)
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID))
	defer span.End()

	rows, err := fetch(ctx, w)
	if err != nil {
		return nil, err
	}
	out := make([]O, 0, len(rows))
	for _, r := range rows {
		out = append(out, conv(r))
	}
	return out, nil
}

// ListSingleSpanTraces groups the traces that have exactly one span by that
// span's name.
func (s *QueryService) ListSingleSpanTraces(ctx context.Context, q TraceHealthQuery) ([]SingleSpanTraceGroup, error) {
	return healthView(ctx, q, "single_span_traces", s.repo.QuerySingleSpanTraceGroups,
		func(r repository.SingleSpanTraceGroup) SingleSpanTraceGroup { return SingleSpanTraceGroup(r) })
}

// ListSpanNames returns the count and root_count of each span name, so entry
// points separate from internal helpers.
func (s *QueryService) ListSpanNames(ctx context.Context, q TraceHealthQuery) ([]SpanNameSummary, error) {
	return healthView(ctx, q, "span_names", s.repo.QuerySpanNameSummary,
		func(r repository.SpanNameSummary) SpanNameSummary { return SpanNameSummary(r) })
}

// ListRootlessTraces returns the traces with no root span in the window and
// their total count. Traces whose summary predates the lazy structure backfill
// are not included until the backfill reaches them.
func (s *QueryService) ListRootlessTraces(ctx context.Context, q TraceHealthQuery) (*RootlessTraces, error) {
	w, err := q.window()
	if err != nil {
		return nil, err
	}
	ctx, span := tracer.Start(ctx, "query.trace_health.rootless_traces")
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID))
	defer span.End()

	total, err := s.repo.RootlessTraceCount(ctx, w)
	if err != nil {
		return nil, err
	}
	noRoot := false
	traces, err := s.SearchTraces(ctx, TraceSearchFilter{
		ProjectID: q.ProjectID,
		HasRoot:   &noRoot,
		From:      w.From,
		To:        w.To,
		Limit:     w.Limit,
	})
	if err != nil {
		return nil, err
	}
	return &RootlessTraces{Total: total, Traces: traces}, nil
}
