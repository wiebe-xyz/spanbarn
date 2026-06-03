package service

import (
	"context"
	"log/slog"
	"net/url"
	"sort"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

var tracer = otel.Tracer("spanbarn/query")

// QueryRepository defines the data access methods needed by QueryService.
type QueryRepository interface {
	QueryAggregates(filter repository.AggregateFilter) ([]repository.Aggregate, error)
	QuerySpans(filter repository.SpanFilter) ([]repository.Span, error)
	SearchTraceSummaries(filter repository.SpanFilter, minSpans int) ([]repository.TraceSummaryRow, error)
	QueryRootSpanGroups(ctx context.Context, f repository.SpanFilter) ([]repository.RootSpanGroup, error)
	GetTraceByID(traceID string) ([]repository.Span, error)
	QueryErrorSamples(filter repository.SpanFilter) ([]repository.Span, error)
	ExcludedOperations(projectID int64) ([]string, error)
	QueryServiceStatsFromSpans(projectID int64, from, to time.Time, kind string) ([]repository.ServiceStats, error)
	QueryOperationStatsFromSpans(projectID int64, service string, from, to time.Time, kind string) ([]repository.OperationStats, error)
	QuerySpanTimeseries(projectID int64, service, operation string, from, to time.Time, intervalSec int64) ([]repository.SpanBucket, error)
	QueryPromptRecords(filter repository.PromptFilter) ([]repository.PromptRecord, error)
	GetSpansBySpanIDs(spanIDs []string) ([]repository.Span, error)
	StreamSpans(filter repository.SpanFilter, fn func(repository.Span) error) error
	QueryWebVitals(service string, from, to time.Time) ([]repository.WebVitalRow, error)
	QueryWebVitalsTimeseries(service, page, metric string, from, to time.Time, intervalSec int64) ([]repository.WebVitalBucket, error)
}

// QueryService implements query logic for the dashboard API.
// Methods are organized into focused files:
//   - query_services.go      — ListServices, ListOperations, GetTimeseries
//   - query_traces.go        — SearchTraces, GetTrace
//   - query_dependencies.go  — ListDependencies
type QueryService struct {
	repo       QueryRepository
	cache      *cache.Cache
	logger     *slog.Logger
	sampleRate float64
}

// NewQueryService creates a new QueryService. sampleRate is the fraction of
// non-error spans that are ingested (e.g. 0.01 for 1%). When < 1.0, ok-span
// counts are inflated by 1/sampleRate so that error rates reflect the true
// population rather than only the sampled spans.
func NewQueryService(repo QueryRepository, logger *slog.Logger, sampleRate float64) *QueryService {
	if logger == nil {
		logger = slog.Default()
	}
	if sampleRate <= 0 || sampleRate > 1 {
		sampleRate = 1.0
	}
	return &QueryService{repo: repo, logger: logger, sampleRate: sampleRate}
}

// inflateCount returns the estimated true span population given sampled counts.
// errorCount is assumed to be always-sampled; only ok spans are scaled.
func inflateCount(count, errorCount int64, sampleRate float64) int64 {
	if sampleRate >= 1.0 {
		return count
	}
	okCount := count - errorCount
	if okCount < 0 {
		okCount = 0
	}
	return errorCount + int64(float64(okCount)/sampleRate)
}

func (s *QueryService) SetCache(c *cache.Cache) {
	s.cache = c
}

// Cache exposes the underlying cache instance for handlers outside the service.
func (s *QueryService) Cache() *cache.Cache {
	return s.cache
}

// --- Shared helpers ---

func computePercentiles(durations []int64) (p50, p95, p99 int64) {
	if len(durations) == 0 {
		return 0, 0, 0
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50 = durations[percentileIndex(len(durations), 50)]
	p95 = durations[percentileIndex(len(durations), 95)]
	p99 = durations[percentileIndex(len(durations), 99)]
	return
}

func percentileIndex(n, pct int) int {
	idx := (n * pct) / 100
	if idx >= n {
		idx = n - 1
	}
	return idx
}

func getStringAttr(attrs map[string]any, key string) (string, bool) {
	v, ok := attrs[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

func extractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
