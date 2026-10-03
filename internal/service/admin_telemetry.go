package service

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// LogsRepository is the storage LogsService needs.
type LogsRepository interface {
	QueryLogs(ctx context.Context, f repository.LogFilter) ([]repository.LogRow, int, error)
	LogHistogram(ctx context.Context, f repository.LogFilter, bucketSecs int) ([]repository.LogHistogramBucket, error)
	ListPinnedTraces(ctx context.Context, projectID int64) ([]repository.PinnedTrace, error)
	PinTrace(ctx context.Context, projectID int64, traceID, label string) error
	UnpinTrace(ctx context.Context, projectID int64, traceID string) error
}

// LogsService reads logs and manages pinned traces.
type LogsService struct{ repo LogsRepository }

func NewLogsService(repo LogsRepository) *LogsService { return &LogsService{repo: repo} }

// Query returns the matching log rows and the total match count.
func (s *LogsService) Query(ctx context.Context, f LogFilter) ([]repository.LogRow, int, error) {
	return s.repo.QueryLogs(ctx, f)
}

func (s *LogsService) Histogram(ctx context.Context, f LogFilter, bucketSecs int) ([]repository.LogHistogramBucket, error) {
	return s.repo.LogHistogram(ctx, f, bucketSecs)
}

func (s *LogsService) ListPinned(ctx context.Context, projectID int64) ([]repository.PinnedTrace, error) {
	return s.repo.ListPinnedTraces(ctx, projectID)
}

func (s *LogsService) Pin(ctx context.Context, projectID int64, traceID, label string) error {
	return s.repo.PinTrace(ctx, projectID, traceID, label)
}

func (s *LogsService) Unpin(ctx context.Context, projectID int64, traceID string) error {
	return s.repo.UnpinTrace(ctx, projectID, traceID)
}

// MetricsRepository is the storage MetricsService needs.
type MetricsRepository interface {
	ListMetricNames(ctx context.Context, projectID int64, from, to time.Time) ([]string, error)
	ListMetricCatalog(ctx context.Context, projectID int64, from, to time.Time) ([]repository.MetricCatalogEntry, error)
	QueryMetricSeries(ctx context.Context, f repository.MetricFilter) ([]repository.MetricRow, error)
	QueryMetricRollups(ctx context.Context, f repository.MetricRollupFilter) ([]repository.MetricRollup, error)
	QueryCoarseRollups(ctx context.Context, f repository.CoarseRollupFilter) ([]repository.MetricRollup, error)
	QueryProjectRollups(ctx context.Context, projectID int64, from, to time.Time, limit int) ([]repository.MetricRollup, error)
}

// MetricsService reads metric series, rollups and the metric catalog.
type MetricsService struct{ repo MetricsRepository }

func NewMetricsService(repo MetricsRepository) *MetricsService { return &MetricsService{repo: repo} }

func (s *MetricsService) Names(ctx context.Context, projectID int64, from, to time.Time) ([]string, error) {
	return s.repo.ListMetricNames(ctx, projectID, from, to)
}

func (s *MetricsService) Catalog(ctx context.Context, projectID int64, from, to time.Time) ([]MetricCatalogEntry, error) {
	return s.repo.ListMetricCatalog(ctx, projectID, from, to)
}

func (s *MetricsService) Series(ctx context.Context, f MetricFilter) ([]MetricRow, error) {
	return s.repo.QueryMetricSeries(ctx, f)
}

// Rollups returns the 5-minute tier rollups matching f.
func (s *MetricsService) Rollups(ctx context.Context, f MetricRollupFilter) ([]MetricRollup, error) {
	return s.repo.QueryMetricRollups(ctx, f)
}

// CoarseRollups returns the 1h, 1d, 1w and 1mo tier rollups matching f.
func (s *MetricsService) CoarseRollups(ctx context.Context, f CoarseRollupFilter) ([]MetricRollup, error) {
	return s.repo.QueryCoarseRollups(ctx, f)
}

func (s *MetricsService) ProjectRollups(ctx context.Context, projectID int64, from, to time.Time, limit int) ([]MetricRollup, error) {
	return s.repo.QueryProjectRollups(ctx, projectID, from, to, limit)
}

// Store is every storage capability the API layer's services draw on. The
// API server holds one Store and builds its services from it.
type Store interface {
	AlertRepository
	SavedQueryRepository
	TraceExclusionRepository
	SpanStreamRepository
	SettingsRepository
	ProjectRepository
	LogsRepository
	MetricsRepository
	BoardRepository
	SLORepository
	CalculatedFieldRepository
}

// WarmRepository is the storage the cache warmers need.
type WarmRepository interface {
	ProjectRepository
	SettingsRepository
}
