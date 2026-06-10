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

// SampleRatioLookup returns the configured 1-in-N sampling ratio for a project.
// A ratio of 1 means keep every span; 1000 means keep 1 in 1000.
// Satisfied by *ingest.CachedRatioLookup and *ingest.StaticRatioLookup.
type SampleRatioLookup interface {
	Ratio(ctx context.Context, projectID int64, operation string) int
}

// RecentAggregateQuerier returns synthetic aggregate rows from an in-memory
// accumulator for the recent tail window. Satisfied by *aggregation.Accumulator.
type RecentAggregateQuerier interface {
	QueryRecent(f repository.AggregateFilter) []repository.Aggregate
}

// QueryService implements query logic for the dashboard API.
// Methods are organized into focused files:
//   - query_services.go      — ListServices, ListOperations, GetTimeseries
//   - query_traces.go        — SearchTraces, GetTrace
//   - query_dependencies.go  — ListDependencies
type QueryService struct {
	repo        QueryRepository
	cache       *cache.Cache
	logger      *slog.Logger
	ratioLookup SampleRatioLookup
	accumulator RecentAggregateQuerier // may be nil on reader/standalone pods
}

// NewQueryService creates a new QueryService. ratioLookup is used to read the
// per-project 1-in-N sampling ratio so that error rates and counts are
// corrected for projects using error-biased sampling. Pass nil for no correction.
func NewQueryService(repo QueryRepository, logger *slog.Logger, ratioLookup SampleRatioLookup) *QueryService {
	if logger == nil {
		logger = slog.Default()
	}
	return &QueryService{repo: repo, logger: logger, ratioLookup: ratioLookup}
}

// projectSampleRate returns the effective sample rate (0–1] for a project.
func (s *QueryService) projectSampleRate(ctx context.Context, projectID int64) float64 {
	if s.ratioLookup == nil {
		return 1.0
	}
	ratio := s.ratioLookup.Ratio(ctx, projectID, "")
	if ratio <= 1 {
		return 1.0
	}
	return 1.0 / float64(ratio)
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

// SetAccumulator wires in the in-memory accumulator for recent tail queries.
func (s *QueryService) SetAccumulator(a RecentAggregateQuerier) {
	s.accumulator = a
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
