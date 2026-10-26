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

// MinDashboardWindow is the narrowest range the dashboard serves; below it a
// chart has too few buckets to read.
const MinDashboardWindow = time.Minute

// dashboardTopN is how many groups keep their own series.
const dashboardTopN = 10

// Dashboard results are cached per filter (readCached) so a reload, a second
// viewer or a retry does not scan the raw spans again. The range is snapped
// down to a grain that grows with the window, so requests inside one grain
// share one entry.
const (
	// dashboardFineGrain keeps short windows close to live.
	dashboardFineGrain = 15 * time.Second
	// dashboardCoarseGrain applies to windows of dashboardCoarseFrom or wider.
	// Their scan costs seconds on a small pod and a quarter hour of lag is
	// invisible on a chart of a day, so one entry serves a whole quarter hour
	// and WarmDashboard can fill it ahead of the first viewer.
	dashboardCoarseGrain = 15 * time.Minute
	dashboardCoarseFrom  = 12 * time.Hour
)

// dashboardGrain returns the snapping grain for a window of the given width.
func dashboardGrain(window time.Duration) time.Duration {
	if window >= dashboardCoarseFrom {
		return dashboardCoarseGrain
	}
	return dashboardFineGrain
}

// grain is the snapping grain of q's window.
func (q DashboardQuery) grain() time.Duration { return dashboardGrain(q.To.Sub(q.From)) }

// snapped returns q with the range snapped down to its grain.
func (q DashboardQuery) snapped() DashboardQuery {
	g := q.grain()
	q.From = q.From.Truncate(g)
	q.To = q.To.Truncate(g)
	return q
}

func (q DashboardQuery) cacheKey(kind, group string, rootOnly bool) string {
	return fmt.Sprintf("dashboard:%s:%s:%t:%d:%q:%q:%q:%d:%d:%d:%d",
		kind, group, rootOnly, q.ProjectID, q.Service, q.Name, q.Status,
		q.MinDurationUs, q.MaxDurationUs, q.From.Unix(), q.To.Unix())
}

// DashboardQuery is the filter shared by every dashboard card.
type DashboardQuery struct {
	ProjectID int64
	Service   string
	Name      string
	Status    string
	// MinDurationUs and MaxDurationUs bound the span duration. 0 means no bound.
	MinDurationUs int64
	MaxDurationUs int64
	From, To      time.Time
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
	// Source is SourceRollups (received spans) or SourceSpans (stored spans).
	Source string `json:"source"`
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
	Source          string                     `json:"source"`
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
	Source          string                 `json:"source"`
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
	if window < MinDashboardWindow {
		return 0, fmt.Errorf("%w: range must be at least %s", ErrInvalidDashboardRequest, MinDashboardWindow)
	}
	if q.MinDurationUs < 0 || q.MaxDurationUs < 0 || (q.MaxDurationUs > 0 && q.MaxDurationUs < q.MinDurationUs) {
		return 0, fmt.Errorf("%w: invalid duration bounds", ErrInvalidDashboardRequest)
	}
	if window > MaxDashboardWindow {
		return 0, fmt.Errorf("%w: range is limited to %s", ErrInvalidDashboardRequest, MaxDashboardWindow)
	}
	return dashboardInterval(window), nil
}

// dashboardInterval picks a bucket width that keeps a chart near 60-100 points.
func dashboardInterval(window time.Duration) int64 {
	switch {
	case window <= 15*time.Minute:
		return 15
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
		ProjectID:   q.ProjectID,
		Service:     q.Service,
		Operation:   q.Name,
		Status:      q.Status,
		MinDuration: q.MinDurationUs,
		MaxDuration: q.MaxDurationUs,
		RootOnly:    rootOnly,
		From:        q.From.UTC(),
		To:          q.To.UTC(),
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

	if _, err := q.validate(); err != nil {
		return nil, err
	}
	q = q.snapped()
	return readCached(ctx, s.cache, q.cacheKey("counts", group, rootOnly), q.grain(), func(ctx context.Context) (*DashboardCounts, error) {
		return s.dashboardCountsUncached(ctx, q, group, rootOnly)
	})
}

func (s *QueryService) dashboardCountsUncached(ctx context.Context, q DashboardQuery, group string, rootOnly bool) (*DashboardCounts, error) {
	interval, err := q.validate()
	if err != nil {
		return nil, err
	}
	g, err := dashboardGroup(group, repository.DashboardGroupService, repository.DashboardGroupName, repository.DashboardGroupStatus, repository.DashboardGroupHTTPStatus)
	if err != nil {
		return nil, err
	}
	pts, source, err := s.dashboardCountPoints(ctx, q, interval, g, rootOnly)
	if err != nil {
		s.logger.Error("dashboard counts query failed", "error", err, "group_by", group, "source", source)
		return nil, err
	}
	out := &DashboardCounts{IntervalSeconds: interval, Points: make([]DashboardCountPoint, 0, len(pts)), Source: source}
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

	if _, err := q.validate(); err != nil {
		return nil, err
	}
	q = q.snapped()
	return readCached(ctx, s.cache, q.cacheKey("percentiles", group, false), q.grain(), func(ctx context.Context) (*DashboardPercentiles, error) {
		return s.dashboardPercentilesUncached(ctx, q, group)
	})
}

func (s *QueryService) dashboardPercentilesUncached(ctx context.Context, q DashboardQuery, group string) (*DashboardPercentiles, error) {
	interval, err := q.validate()
	if err != nil {
		return nil, err
	}
	g, err := dashboardGroup(group, repository.DashboardGroupService, repository.DashboardGroupName, repository.DashboardGroupStatus)
	if err != nil {
		return nil, err
	}
	pts, source, err := s.dashboardPercentilePoints(ctx, q, interval, g)
	if err != nil {
		s.logger.Error("dashboard percentiles query failed", "error", err, "group_by", group, "source", source)
		return nil, err
	}
	out := &DashboardPercentiles{IntervalSeconds: interval, Points: make([]DashboardPercentilePoint, 0, len(pts)), Source: source}
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

	if _, err := q.validate(); err != nil {
		return nil, err
	}
	q = q.snapped()
	return readCached(ctx, s.cache, q.cacheKey("heatmap", "", rootOnly), q.grain(), func(ctx context.Context) (*DashboardHeatmap, error) {
		return s.dashboardHeatmapUncached(ctx, q, rootOnly)
	})
}

func (s *QueryService) dashboardHeatmapUncached(ctx context.Context, q DashboardQuery, rootOnly bool) (*DashboardHeatmap, error) {
	interval, err := q.validate()
	if err != nil {
		return nil, err
	}
	cells, source, err := s.dashboardHeatmapCells(ctx, q, interval, rootOnly)
	if err != nil {
		s.logger.Error("dashboard heatmap query failed", "error", err, "source", source)
		return nil, err
	}
	out := &DashboardHeatmap{IntervalSeconds: interval, Cells: make([]DashboardHeatmapCell, 0, len(cells)), Source: source}
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

// WarmDashboard fills the cache for the dashboard's landing view: the default
// 24h window over all projects, every card the page requests. Viewers inside
// the same grain then read cached results instead of paying the cold scans.
// It is a no-op for a result another process already cached.
func (s *QueryService) WarmDashboard(ctx context.Context, now time.Time) error {
	q := DashboardQuery{From: now.Add(-24 * time.Hour), To: now}
	var errs []error
	note := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	_, err := s.GetDashboardCounts(ctx, q, "service", true)
	note(err)
	_, err = s.GetDashboardCounts(ctx, q, "http_status", true)
	note(err)
	_, err = s.GetDashboardHeatmap(ctx, q, true)
	note(err)
	_, err = s.GetDashboardHeatmap(ctx, q, false)
	note(err)
	_, err = s.GetDashboardPercentiles(ctx, q, "service")
	note(err)
	_, err = s.GetDashboardPercentiles(ctx, q, "name")
	note(err)
	return errors.Join(errs...)
}
