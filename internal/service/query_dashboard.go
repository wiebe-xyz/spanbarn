package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ErrInvalidDashboardRequest marks a dashboard request the caller must fix
// (bad range, unknown group). The API maps it to HTTP 400.
var ErrInvalidDashboardRequest = errors.New("invalid dashboard request")

// MaxDashboardWindow is the widest range the dashboard serves. It matches the
// raw-span retention window, because the dashboard reads raw spans.
const MaxDashboardWindow = 48 * time.Hour

// dashboardTopN is how many groups keep their own series.
const dashboardTopN = 10

// DashboardQuery is the filter shared by every dashboard card.
type DashboardQuery struct {
	ProjectID int64
	Service   string
	Name      string
	Status    string
	From, To  time.Time
}

// DashboardCountPoint is one point of a count series.
type DashboardCountPoint struct {
	Time  time.Time `json:"time"`
	Group string    `json:"group"`
	Count int64     `json:"count"`
}

// DashboardCounts is a set of count series sharing one bucket width.
type DashboardCounts struct {
	IntervalSeconds int64                 `json:"intervalSeconds"`
	Points          []DashboardCountPoint `json:"points"`
}

// DashboardPercentilePoint is one point of a duration percentile series, in
// microseconds.
type DashboardPercentilePoint struct {
	Time  time.Time `json:"time"`
	Group string    `json:"group"`
	Count int64     `json:"count"`
	P90Us int64     `json:"p90Us"`
	P95Us int64     `json:"p95Us"`
	P99Us int64     `json:"p99Us"`
}

// DashboardPercentiles is a set of percentile series sharing one bucket width.
type DashboardPercentiles struct {
	IntervalSeconds int64                      `json:"intervalSeconds"`
	Points          []DashboardPercentilePoint `json:"points"`
}

// DashboardHeatmapCell is one populated cell of the duration heatmap. The
// duration bucket covers [LowerUs, UpperUs); Bucket is its index on the
// half-octave scale, so a client can place cells without inverting the edges.
type DashboardHeatmapCell struct {
	Time    time.Time `json:"time"`
	Bucket  int       `json:"bucket"`
	LowerUs int64     `json:"lowerUs"`
	UpperUs int64     `json:"upperUs"`
	Count   int64     `json:"count"`
}

// DashboardHeatmap is the duration distribution over time.
type DashboardHeatmap struct {
	IntervalSeconds int64                  `json:"intervalSeconds"`
	Cells           []DashboardHeatmapCell `json:"cells"`
}

// validate checks the range and returns the bucket width in seconds.
func (q DashboardQuery) validate() (int64, error) {
	if q.From.IsZero() || q.To.IsZero() {
		return 0, fmt.Errorf("%w: from and to are required", ErrInvalidDashboardRequest)
	}
	window := q.To.Sub(q.From)
	if window <= 0 {
		return 0, fmt.Errorf("%w: to must be after from", ErrInvalidDashboardRequest)
	}
	if window > MaxDashboardWindow {
		return 0, fmt.Errorf("%w: range is limited to %s", ErrInvalidDashboardRequest, MaxDashboardWindow)
	}
	return dashboardInterval(window), nil
}

// dashboardInterval picks a bucket width that keeps a chart near 60-100 points.
func dashboardInterval(window time.Duration) int64 {
	switch {
	case window <= time.Hour:
		return 60
	case window <= 4*time.Hour:
		return 300
	case window <= 24*time.Hour:
		return 900
	default:
		return 1800
	}
}

func (q DashboardQuery) spanFilter(rootOnly bool) repository.SpanFilter {
	return repository.SpanFilter{
		ProjectID: q.ProjectID,
		Service:   q.Service,
		Operation: q.Name,
		Status:    q.Status,
		RootOnly:  rootOnly,
		From:      q.From.UTC(),
		To:        q.To.UTC(),
	}
}

// dashboardGroup maps a request value to a repository group.
func dashboardGroup(group string, allowed ...repository.DashboardGroup) (repository.DashboardGroup, error) {
	g := repository.DashboardGroup(group)
	for _, a := range allowed {
		if g == a {
			return g, nil
		}
	}
	return "", fmt.Errorf("%w: unsupported group_by %q", ErrInvalidDashboardRequest, group)
}

// GetDashboardCounts counts spans (or only root spans, i.e. traces) per bucket,
// grouped by service or HTTP status code.
func (s *QueryService) GetDashboardCounts(ctx context.Context, q DashboardQuery, group string, rootOnly bool) (*DashboardCounts, error) {
	_, span := tracer.Start(ctx, "query.dashboard_counts")
	span.SetAttributes(attribute.String("group_by", group))
	defer span.End()

	interval, err := q.validate()
	if err != nil {
		return nil, err
	}
	g, err := dashboardGroup(group, repository.DashboardGroupService, repository.DashboardGroupHTTPStatus)
	if err != nil {
		return nil, err
	}
	pts, err := s.repo.QueryDashboardCounts(q.spanFilter(rootOnly), interval, g, dashboardTopN)
	if err != nil {
		s.logger.Error("dashboard counts query failed", "error", err, "group_by", group)
		return nil, err
	}
	out := &DashboardCounts{IntervalSeconds: interval, Points: make([]DashboardCountPoint, 0, len(pts))}
	for _, p := range pts {
		out.Points = append(out.Points, DashboardCountPoint{Time: p.Bucket, Group: p.Group, Count: p.Count})
	}
	return out, nil
}

// GetDashboardPercentiles returns P90/P95/P99 duration per bucket, grouped by
// service or span name.
func (s *QueryService) GetDashboardPercentiles(ctx context.Context, q DashboardQuery, group string) (*DashboardPercentiles, error) {
	_, span := tracer.Start(ctx, "query.dashboard_percentiles")
	span.SetAttributes(attribute.String("group_by", group))
	defer span.End()

	interval, err := q.validate()
	if err != nil {
		return nil, err
	}
	g, err := dashboardGroup(group, repository.DashboardGroupService, repository.DashboardGroupName)
	if err != nil {
		return nil, err
	}
	pts, err := s.repo.QueryDashboardPercentiles(q.spanFilter(false), interval, g, dashboardTopN)
	if err != nil {
		s.logger.Error("dashboard percentiles query failed", "error", err, "group_by", group)
		return nil, err
	}
	out := &DashboardPercentiles{IntervalSeconds: interval, Points: make([]DashboardPercentilePoint, 0, len(pts))}
	for _, p := range pts {
		out.Points = append(out.Points, DashboardPercentilePoint{
			Time: p.Bucket, Group: p.Group, Count: p.Count,
			P90Us: p.P90Us, P95Us: p.P95Us, P99Us: p.P99Us,
		})
	}
	return out, nil
}

// GetDashboardHeatmap returns the duration distribution per bucket for all
// spans, or only root spans when rootOnly is set.
func (s *QueryService) GetDashboardHeatmap(ctx context.Context, q DashboardQuery, rootOnly bool) (*DashboardHeatmap, error) {
	_, span := tracer.Start(ctx, "query.dashboard_heatmap")
	defer span.End()

	interval, err := q.validate()
	if err != nil {
		return nil, err
	}
	cells, err := s.repo.QueryDashboardHeatmap(q.spanFilter(rootOnly), interval)
	if err != nil {
		s.logger.Error("dashboard heatmap query failed", "error", err)
		return nil, err
	}
	out := &DashboardHeatmap{IntervalSeconds: interval, Cells: make([]DashboardHeatmapCell, 0, len(cells))}
	for _, c := range cells {
		out.Cells = append(out.Cells, DashboardHeatmapCell{
			Time:    c.Bucket,
			Bucket:  c.DurationBucket,
			LowerUs: repository.HeatmapBucketLowerUs(c.DurationBucket),
			UpperUs: repository.HeatmapBucketLowerUs(c.DurationBucket + 1),
			Count:   c.Count,
		})
	}
	return out, nil
}
