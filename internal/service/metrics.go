package service

import (
	"context"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

// MetricsRepository is the storage MetricsService needs.
type MetricsRepository interface {
	ListMetricNames(ctx context.Context, projectID int64, from, to time.Time) ([]string, error)
	ListMetricCatalog(ctx context.Context, projectID int64, from, to time.Time) ([]repository.MetricCatalogEntry, error)
	QueryMetricSeries(ctx context.Context, f repository.MetricFilter) ([]repository.MetricRow, error)
	QueryMetricRollups(ctx context.Context, f repository.MetricRollupFilter) ([]repository.MetricRollup, error)
	QueryCoarseRollups(ctx context.Context, f repository.CoarseRollupFilter) ([]repository.MetricRollup, error)
	ScanProjectRollups(ctx context.Context, projectID, step int64, from, to time.Time, fn func(repository.MetricRollup) error) error
	MetricRollupWatermark(ctx context.Context, step int64) (time.Time, bool, error)
}

// MetricsService reads metric series, rollups and the metric catalog.
type MetricsService struct {
	repo  MetricsRepository
	cache *cache.Cache
}

func NewMetricsService(repo MetricsRepository) *MetricsService { return &MetricsService{repo: repo} }

// SetCache wires in the cache that insights results are kept in.
func (s *MetricsService) SetCache(c *cache.Cache) { s.cache = c }

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

// HourTierThrough reports how far the 5-minute tier has been compacted into the
// hour tier: hour buckets before it are complete, and the 5-minute buckets from
// it on are not in the hour tier yet. ok is false before the first compaction.
func (s *MetricsService) HourTierThrough(ctx context.Context) (time.Time, bool, error) {
	return s.repo.MetricRollupWatermark(ctx, tier.Step5m)
}

// CoarseRollups returns the 1h, 1d, 1w and 1mo tier rollups matching f.
func (s *MetricsService) CoarseRollups(ctx context.Context, f CoarseRollupFilter) ([]MetricRollup, error) {
	return s.repo.QueryCoarseRollups(ctx, f)
}
