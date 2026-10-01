package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const (
	defaultSpanSearchLimit = 50
	maxSpanSearchLimit     = 200
)

// SpanSearchFilter scopes a span list. ProjectID and From are required: the
// filter model reads span attributes, so the scan must stay inside a window.
type SpanSearchFilter struct {
	ProjectID int64
	Expr      *filter.Expr
	From, To  time.Time
	Limit     int
	Offset    int
}

// SpanResult is one span of a span list.
type SpanResult struct {
	TraceID      string          `json:"traceId"`
	SpanID       string          `json:"spanId"`
	ParentSpanID string          `json:"parentSpanId"`
	Name         string          `json:"name"`
	Service      string          `json:"service"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	StartTime    time.Time       `json:"startTime"`
	DurationUs   int64           `json:"durationUs"`
	Attributes   json.RawMessage `json:"attributes"`
}

// SearchSpans lists spans matching a filter expression, newest first.
func (s *QueryService) SearchSpans(ctx context.Context, f SpanSearchFilter) ([]SpanResult, error) {
	_, span := tracer.Start(ctx, "query.search_spans")
	span.SetAttributes(attribute.Int64("project_id", f.ProjectID), attribute.Int("limit", f.Limit))
	defer span.End()

	if f.ProjectID == 0 {
		return nil, fmt.Errorf("%w: project_id is required", filter.ErrInvalid)
	}
	if f.From.IsZero() {
		return nil, fmt.Errorf("%w: from is required", filter.ErrInvalid)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultSpanSearchLimit
	}
	if limit > maxSpanSearchLimit {
		limit = maxSpanSearchLimit
	}
	rows, err := s.repo.QuerySpans(repository.SpanFilter{
		ProjectID: f.ProjectID,
		Expr:      f.Expr,
		From:      f.From,
		To:        f.To,
		Limit:     limit,
		Offset:    f.Offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SpanResult, 0, len(rows))
	for _, r := range rows {
		attrs := json.RawMessage("null")
		if json.Valid([]byte(r.Attributes)) && r.Attributes != "" {
			attrs = json.RawMessage(r.Attributes)
		}
		out = append(out, SpanResult{
			TraceID: r.TraceID, SpanID: r.SpanID, ParentSpanID: r.ParentSpanID,
			Name: r.Name, Service: r.Service, Kind: r.Kind, Status: r.Status,
			StartTime: time.UnixMicro(r.StartTimeUs), DurationUs: r.DurationUs,
			Attributes: attrs,
		})
	}
	return out, nil
}
