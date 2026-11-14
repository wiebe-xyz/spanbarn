package service

import (
	"context"

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
