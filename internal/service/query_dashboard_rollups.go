package service

import (
	"context"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Dashboard cards read span_rollups when their chart buckets are whole
// 5-minute buckets (windows over an hour) and the rollups cover the whole
// range; otherwise raw spans. Each result names its source, because rollups
// count the spans the writer received and raw spans the ones it stored.

// dashboardRollupQuery returns the span_rollups read for q, or ok false when q
// must read raw spans: a status or duration band the rollups do not hold, or
// a range they do not cover at this chart bucket width.
func (s *QueryService) dashboardRollupQuery(ctx context.Context, q DashboardQuery, interval int64, rootOnly bool) (repository.DashboardRollupQuery, bool) {
	if !rollupStatus(q.Status) {
		return repository.DashboardRollupQuery{}, false
	}
	lo, hi, ok := rollupBand(q.MinDurationUs, q.MaxDurationUs)
	if !ok {
		return repository.DashboardRollupQuery{}, false
	}
	segs, ok := s.spanRollupPlan(ctx, q.From, q.To, interval)
	if !ok {
		return repository.DashboardRollupQuery{}, false
	}
	return repository.DashboardRollupQuery{
		Scan: repository.SpanRollupScan{
			Segments: segs, ProjectID: q.ProjectID, Service: q.Service, Name: q.Name,
			Status: q.Status, RootOnly: rootOnly,
		},
		Interval: interval, BinLo: lo, BinHi: hi,
	}, true
}

// dashboardCountPoints counts from span_rollups where it can. The rollups
// hold a response code only on root rows, and only while the writers split by
// it, so grouping by HTTP status reads raw spans otherwise.
func (s *QueryService) dashboardCountPoints(ctx context.Context, q DashboardQuery, interval int64, g repository.DashboardGroup, rootOnly bool) ([]repository.DashboardCountPoint, string, error) {
	if g != repository.DashboardGroupHTTPStatus || (rootOnly && s.rollupReads.HTTPStatus) {
		if rq, ok := s.dashboardRollupQuery(ctx, q, interval, rootOnly); ok {
			pts, err := s.repo.QueryDashboardCountsFromRollups(ctx, rq, g, dashboardTopN)
			return pts, SourceRollups, err
		}
	}
	pts, err := s.repo.QueryDashboardCounts(ctx, q.spanFilter(rootOnly), interval, g, dashboardTopN)
	return pts, SourceSpans, err
}

func (s *QueryService) dashboardPercentilePoints(ctx context.Context, q DashboardQuery, interval int64, g repository.DashboardGroup) ([]repository.DashboardPercentilePoint, string, error) {
	if rq, ok := s.dashboardRollupQuery(ctx, q, interval, false); ok {
		pts, err := s.repo.QueryDashboardPercentilesFromRollups(ctx, rq, g, dashboardTopN)
		return pts, SourceRollups, err
	}
	pts, err := s.repo.QueryDashboardPercentiles(ctx, q.spanFilter(false), interval, g, dashboardTopN)
	return pts, SourceSpans, err
}

func (s *QueryService) dashboardHeatmapCells(ctx context.Context, q DashboardQuery, interval int64, rootOnly bool) ([]repository.DashboardHeatmapCell, string, error) {
	if rq, ok := s.dashboardRollupQuery(ctx, q, interval, rootOnly); ok {
		cells, err := s.repo.QueryDashboardHeatmapFromRollups(ctx, rq)
		return cells, SourceRollups, err
	}
	cells, err := s.repo.QueryDashboardHeatmap(ctx, q.spanFilter(rootOnly), interval)
	return cells, SourceSpans, err
}
