package service

import (
	"log/slog"
	"net/url"
	"sort"
	"strings"
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
	GetTraceByID(traceID string) ([]repository.Span, error)
	QueryErrorSamples(filter repository.SpanFilter) ([]repository.Span, error)
	QueryServiceStatsFromSpans(projectID int64, from, to time.Time) ([]repository.ServiceStats, error)
	QueryOperationStatsFromSpans(projectID int64, service string, from, to time.Time) ([]repository.OperationStats, error)
	QuerySpanTimeseries(projectID int64, service, operation string, from, to time.Time, intervalSec int64) ([]repository.SpanBucket, error)
	QueryPromptRecords(filter repository.PromptFilter) ([]repository.PromptRecord, error)
	GetSpansBySpanIDs(spanIDs []string) ([]repository.Span, error)
	StreamSpans(filter repository.SpanFilter, fn func(repository.Span) error) error
	QueryWebVitals(from, to time.Time) ([]repository.WebVitalRow, error)
}

// QueryService implements query logic for the dashboard API.
// Methods are organized into focused files:
//   - query_services.go      — ListServices, ListOperations, GetTimeseries
//   - query_traces.go        — SearchTraces, GetTrace
//   - query_dependencies.go  — ListDependencies
type QueryService struct {
	repo   QueryRepository
	cache  *cache.Cache
	logger *slog.Logger
}

// NewQueryService creates a new QueryService.
func NewQueryService(repo QueryRepository, logger *slog.Logger) *QueryService {
	if logger == nil {
		logger = slog.Default()
	}
	return &QueryService{repo: repo, logger: logger}
}

func (s *QueryService) SetCache(c *cache.Cache) {
	s.cache = c
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
	host := u.Hostname()
	if host == "" {
		parts := strings.SplitN(rawURL, "/", 4)
		if len(parts) >= 3 {
			return parts[2]
		}
	}
	return host
}
