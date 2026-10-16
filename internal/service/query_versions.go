package service

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
)

// VersionSummary holds the metrics of one service.version of a service.
type VersionSummary struct {
	Version    string    `json:"version"`
	FirstSeen  time.Time `json:"firstSeen"`
	LastSeen   time.Time `json:"lastSeen"`
	SpanCount  int64     `json:"spanCount"`
	ErrorCount int64     `json:"errorCount"`
	ErrorRate  float64   `json:"errorRate"`
	P50Us      int64     `json:"p50Us"`
	P95Us      int64     `json:"p95Us"`
	P99Us      int64     `json:"p99Us"`
}

// ListServiceVersions returns the entry-point metrics of a service per
// service.version, newest version first. Counts are raw spans in the window,
// so they are not scaled by the project's sampling ratio. The error rate and
// the percentiles do not depend on that scaling.
func (s *QueryService) ListServiceVersions(ctx context.Context, projectID int64, service string, from, to time.Time) ([]VersionSummary, error) {
	_, span := tracer.Start(ctx, "query.list_service_versions")
	span.SetAttributes(attribute.String("service", service))
	defer span.End()

	cacheKey := fmt.Sprintf("versions:%d:%s:%d:%d", projectID, service, from.Truncate(time.Minute).Unix(), to.Truncate(time.Minute).Unix())
	if cached, ok := cache.Get[[]VersionSummary](s.cache, ctx, cacheKey); ok {
		return cached, nil
	}

	stats, err := s.repo.QueryVersionStatsFromSpans(projectID, service, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]VersionSummary, 0, len(stats))
	for _, v := range stats {
		var rate float64
		if v.Count > 0 {
			rate = float64(v.ErrorCount) / float64(v.Count)
		}
		out = append(out, VersionSummary{
			Version: v.Version, FirstSeen: v.FirstSeen, LastSeen: v.LastSeen,
			SpanCount: v.Count, ErrorCount: v.ErrorCount, ErrorRate: rate,
			P50Us: v.P50Us, P95Us: v.P95Us, P99Us: v.P99Us,
		})
	}
	cache.Set(s.cache, ctx, cacheKey, out)
	return out, nil
}
