package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

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
}

// QueryService implements query logic for the dashboard API.
type QueryService struct {
	repo   QueryRepository
	logger *slog.Logger
}

// NewQueryService creates a new QueryService.
func NewQueryService(repo QueryRepository, logger *slog.Logger) *QueryService {
	if logger == nil {
		logger = slog.Default()
	}
	return &QueryService{repo: repo, logger: logger}
}

// ListServices returns aggregated metrics per service for the given time range.
// It queries both the aggregates table (for older data) and the raw spans table
// (for recent data not yet aggregated), then merges the results.
func (s *QueryService) ListServices(ctx context.Context, projectID int64, from, to time.Time) ([]ServiceSummary, error) {
	_, span := tracer.Start(ctx, "query.list_services")
	defer span.End()

	// Query pre-computed aggregates.
	aggs, err := s.repo.QueryAggregates(repository.AggregateFilter{
		ProjectID: projectID,
		From:      from,
		To:        to,
		Limit:     10000,
	})
	if err != nil {
		return nil, err
	}

	// Group aggregates by service.
	type svcStats struct {
		count      int64
		errorCount int64
		p50Sum     int64
		p95Sum     int64
		p99Sum     int64
		buckets    int64
	}
	byService := make(map[string]*svcStats)
	for _, a := range aggs {
		st, ok := byService[a.Service]
		if !ok {
			st = &svcStats{}
			byService[a.Service] = st
		}
		st.count += a.Count
		st.errorCount += a.ErrorCount
		st.p50Sum += a.P50Us * a.Count
		st.p95Sum += a.P95Us * a.Count
		st.p99Sum += a.P99Us * a.Count
		st.buckets += a.Count
	}

	// Also query recent raw spans for services not yet aggregated.
	spanStats, err := s.repo.QueryServiceStatsFromSpans(projectID, from, to)
	if err != nil {
		s.logger.Warn("failed to query service stats from spans", "error", err)
		// Fall through with aggregate-only data.
	}

	// Merge span stats into the result. For services that appear in both,
	// we combine the counts. The raw span data has accurate percentiles
	// while the aggregate data uses weighted averages.
	type mergedStats struct {
		count      int64
		errorCount int64
		// From aggregates (weighted).
		aggP50Sum int64
		aggP95Sum int64
		aggP99Sum int64
		aggCount  int64
		// From raw spans (exact durations for percentile computation).
		spanDurations []int64
	}
	merged := make(map[string]*mergedStats)

	for svc, st := range byService {
		merged[svc] = &mergedStats{
			count:      st.count,
			errorCount: st.errorCount,
			aggP50Sum:  st.p50Sum,
			aggP95Sum:  st.p95Sum,
			aggP99Sum:  st.p99Sum,
			aggCount:   st.buckets,
		}
	}

	for _, ss := range spanStats {
		ms, ok := merged[ss.Service]
		if !ok {
			ms = &mergedStats{}
			merged[ss.Service] = ms
		}
		ms.count += ss.Count
		ms.errorCount += ss.ErrorCount
		ms.spanDurations = ss.Durations
	}

	result := make([]ServiceSummary, 0, len(merged))
	for svc, ms := range merged {
		var errorRate float64
		if ms.count > 0 {
			errorRate = float64(ms.errorCount) / float64(ms.count)
		}

		var p50, p95, p99 int64
		if len(ms.spanDurations) > 0 {
			// Prefer exact percentiles from raw spans when available.
			p50, p95, p99 = computePercentiles(ms.spanDurations)
		} else if ms.aggCount > 0 {
			// Fall back to weighted average from aggregates.
			p50 = ms.aggP50Sum / ms.aggCount
			p95 = ms.aggP95Sum / ms.aggCount
			p99 = ms.aggP99Sum / ms.aggCount
		}

		result = append(result, ServiceSummary{
			Service:    svc,
			SpanCount:  ms.count,
			ErrorCount: ms.errorCount,
			ErrorRate:  errorRate,
			P50Us:      p50,
			P95Us:      p95,
			P99Us:      p99,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].SpanCount > result[j].SpanCount
	})

	return result, nil
}

// ListOperations returns aggregated metrics per operation for a service.
func (s *QueryService) ListOperations(ctx context.Context, projectID int64, service string, from, to time.Time) ([]OperationSummary, error) {
	_, span := tracer.Start(ctx, "query.list_operations")
	span.SetAttributes(attribute.String("service", service))
	defer span.End()

	aggs, err := s.repo.QueryAggregates(repository.AggregateFilter{
		ProjectID: projectID,
		Service:   service,
		From:      from,
		To:        to,
		Limit:     10000,
	})
	if err != nil {
		return nil, err
	}

	// Group by operation+resource+kind.
	type opKey struct {
		operation, resource, kind string
	}
	type opStats struct {
		count      int64
		errorCount int64
		p50Sum     int64
		p95Sum     int64
		p99Sum     int64
		total      int64
	}
	byOp := make(map[opKey]*opStats)
	for _, a := range aggs {
		k := opKey{a.Operation, a.Resource, a.Kind}
		st, ok := byOp[k]
		if !ok {
			st = &opStats{}
			byOp[k] = st
		}
		st.count += a.Count
		st.errorCount += a.ErrorCount
		st.p50Sum += a.P50Us * a.Count
		st.p95Sum += a.P95Us * a.Count
		st.p99Sum += a.P99Us * a.Count
		st.total += a.Count
	}

	result := make([]OperationSummary, 0, len(byOp))
	for k, st := range byOp {
		var errorRate float64
		if st.count > 0 {
			errorRate = float64(st.errorCount) / float64(st.count)
		}
		var p50, p95, p99 int64
		if st.total > 0 {
			p50 = st.p50Sum / st.total
			p95 = st.p95Sum / st.total
			p99 = st.p99Sum / st.total
		}
		result = append(result, OperationSummary{
			Operation:  k.operation,
			Resource:   k.resource,
			Kind:       k.kind,
			SpanCount:  st.count,
			ErrorCount: st.errorCount,
			ErrorRate:  errorRate,
			P50Us:      p50,
			P95Us:      p95,
			P99Us:      p99,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].SpanCount > result[j].SpanCount
	})

	return result, nil
}

// GetTimeseries returns bucketed timeseries data for a specific operation.
func (s *QueryService) GetTimeseries(ctx context.Context, projectID int64, service, operation string, from, to time.Time, interval time.Duration) ([]TimeseriesBucket, error) {
	_, span := tracer.Start(ctx, "query.get_timeseries")
	span.SetAttributes(
		attribute.String("service", service),
		attribute.String("operation", operation),
	)
	defer span.End()

	aggs, err := s.repo.QueryAggregates(repository.AggregateFilter{
		ProjectID: projectID,
		Service:   service,
		Operation: operation,
		From:      from,
		To:        to,
		Limit:     100000,
	})
	if err != nil {
		return nil, err
	}

	// Group by truncated bucket time.
	type bucketStats struct {
		count      int64
		errorCount int64
		p50Sum     int64
		p95Sum     int64
		p99Sum     int64
		total      int64
	}
	byBucket := make(map[time.Time]*bucketStats)
	for _, a := range aggs {
		b := a.Bucket.Truncate(interval)
		st, ok := byBucket[b]
		if !ok {
			st = &bucketStats{}
			byBucket[b] = st
		}
		st.count += a.Count
		st.errorCount += a.ErrorCount
		st.p50Sum += a.P50Us * a.Count
		st.p95Sum += a.P95Us * a.Count
		st.p99Sum += a.P99Us * a.Count
		st.total += a.Count
	}

	result := make([]TimeseriesBucket, 0, len(byBucket))
	for b, st := range byBucket {
		var p50, p95, p99 int64
		if st.total > 0 {
			p50 = st.p50Sum / st.total
			p95 = st.p95Sum / st.total
			p99 = st.p99Sum / st.total
		}
		result = append(result, TimeseriesBucket{
			Bucket:     b,
			Count:      st.count,
			ErrorCount: st.errorCount,
			P50Us:      p50,
			P95Us:      p95,
			P99Us:      p99,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Bucket.Before(result[j].Bucket)
	})

	return result, nil
}

// SearchTraces searches for traces matching the given filter.
func (s *QueryService) SearchTraces(ctx context.Context, filter TraceSearchFilter) ([]TraceSummary, error) {
	_, span := tracer.Start(ctx, "query.search_traces")
	span.SetAttributes(
		attribute.String("service", filter.Service),
		attribute.String("operation", filter.Operation),
		attribute.Int("limit", filter.Limit),
	)
	defer span.End()

	sf := repository.SpanFilter{
		ProjectID:   filter.ProjectID,
		Service:     filter.Service,
		Operation:   filter.Operation,
		Status:      filter.Status,
		MinDuration: filter.MinDurationUs,
		From:        filter.From,
		To:          filter.To,
		Limit:       filter.Limit * 5, // fetch more to group by trace
		Offset:      0,
	}

	spans, err := s.repo.QuerySpans(sf)
	if err != nil {
		return nil, err
	}

	// Also fetch error samples.
	errorSpans, err := s.repo.QueryErrorSamples(sf)
	if err != nil {
		s.logger.Warn("failed to query error samples", "error", err)
	} else {
		spans = append(spans, errorSpans...)
	}

	// Group by trace_id.
	type traceInfo struct {
		spans []repository.Span
	}
	byTrace := make(map[string]*traceInfo)
	// Use a slice to preserve insertion order.
	var traceOrder []string
	for _, sp := range spans {
		ti, ok := byTrace[sp.TraceID]
		if !ok {
			ti = &traceInfo{}
			byTrace[sp.TraceID] = ti
			traceOrder = append(traceOrder, sp.TraceID)
		}
		ti.spans = append(ti.spans, sp)
	}

	// Build summaries.
	var result []TraceSummary
	for _, traceID := range traceOrder {
		ti := byTrace[traceID]

		// Find root span (no parent).
		var root *repository.Span
		for i := range ti.spans {
			if ti.spans[i].ParentSpanID == "" {
				root = &ti.spans[i]
				break
			}
		}
		if root == nil && len(ti.spans) > 0 {
			root = &ti.spans[0]
		}
		if root == nil {
			continue
		}

		// Determine overall status: if any span has error status, trace is error.
		status := "ok"
		for _, sp := range ti.spans {
			if sp.Status == "error" {
				status = "error"
				break
			}
		}

		// Deduplicate span count by span_id.
		seenSpans := make(map[string]bool)
		for _, sp := range ti.spans {
			seenSpans[sp.SpanID] = true
		}

		result = append(result, TraceSummary{
			TraceID:      traceID,
			RootSpanName: root.Name,
			RootService:  root.Service,
			DurationUs:   root.DurationUs,
			SpanCount:    len(seenSpans),
			Status:       status,
			StartTime:    time.UnixMicro(root.StartTimeUs),
		})
	}

	// Apply offset and limit.
	if filter.Offset > 0 && filter.Offset < len(result) {
		result = result[filter.Offset:]
	} else if filter.Offset >= len(result) {
		return []TraceSummary{}, nil
	}

	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}

	return result, nil
}

// GetTrace returns full trace detail for a given trace ID.
func (s *QueryService) GetTrace(ctx context.Context, traceID string) (*TraceDetail, error) {
	_, span := tracer.Start(ctx, "query.get_trace")
	span.SetAttributes(attribute.String("trace_id", traceID))
	defer span.End()

	spans, err := s.repo.GetTraceByID(traceID)
	if err != nil {
		return nil, err
	}

	// Also check error_samples.
	errorSpans, err := s.repo.QueryErrorSamples(repository.SpanFilter{
		Limit: 1000,
	})
	if err != nil {
		s.logger.Warn("failed to query error samples for trace", "error", err)
	} else {
		// Add error sample spans that belong to this trace.
		for _, sp := range errorSpans {
			if sp.TraceID == traceID {
				spans = append(spans, sp)
			}
		}
	}

	// Deduplicate by span_id.
	seen := make(map[string]bool)
	var unique []repository.Span
	for _, sp := range spans {
		if !seen[sp.SpanID] {
			seen[sp.SpanID] = true
			unique = append(unique, sp)
		}
	}
	spans = unique

	if len(spans) == 0 {
		return nil, nil
	}

	// Find root span.
	var root *repository.Span
	for i := range spans {
		if spans[i].ParentSpanID == "" {
			root = &spans[i]
			break
		}
	}
	if root == nil {
		root = &spans[0]
	}

	// Calculate total duration from root.
	return &TraceDetail{
		TraceID:    traceID,
		Spans:      spans,
		DurationUs: root.DurationUs,
		Service:    root.Service,
		Name:       root.Name,
	}, nil
}

// ListDependencies extracts dependency information from client-kind spans.
func (s *QueryService) ListDependencies(ctx context.Context, projectID int64, from, to time.Time, service string) ([]DependencySummary, error) {
	_, span := tracer.Start(ctx, "query.list_dependencies")
	defer span.End()

	sf := repository.SpanFilter{
		ProjectID: projectID,
		Service:   service,
		From:      from,
		To:        to,
		Limit:     10000,
	}

	spans, err := s.repo.QuerySpans(sf)
	if err != nil {
		return nil, err
	}

	// Filter to client-kind spans and extract targets.
	type depKey struct {
		target     string
		targetType string
	}
	type depStats struct {
		count      int64
		errorCount int64
		durations  []int64
	}
	byDep := make(map[depKey]*depStats)

	addDep := func(target, targetType string, sp repository.Span) {
		k := depKey{target, targetType}
		st, ok := byDep[k]
		if !ok {
			st = &depStats{}
			byDep[k] = st
		}
		st.count++
		if sp.Status == "error" {
			st.errorCount++
		}
		st.durations = append(st.durations, sp.DurationUs)
	}

	for _, sp := range spans {
		if sp.Kind == "client" || sp.Kind == "CLIENT" {
			target, targetType := extractDependencyTarget(sp.Attributes)
			if target != "" {
				addDep(target, targetType, sp)
			}
		}
	}

	// Extract cross-service dependencies from parent-child relationships.
	// Build a map of spanID -> span for lookup.
	spanByID := make(map[string]*repository.Span, len(spans))
	for i := range spans {
		spanByID[spans[i].SpanID] = &spans[i]
	}
	// Track service-to-service edges we've already counted per span to avoid duplicates.
	for _, sp := range spans {
		if sp.ParentSpanID == "" {
			continue
		}
		parent, ok := spanByID[sp.ParentSpanID]
		if !ok {
			continue
		}
		if parent.Service != "" && sp.Service != "" && parent.Service != sp.Service {
			addDep(sp.Service, "service", *parent)
		}
	}

	result := make([]DependencySummary, 0, len(byDep))
	for k, st := range byDep {
		var errorRate float64
		if st.count > 0 {
			errorRate = float64(st.errorCount) / float64(st.count)
		}
		p50, p95, p99 := computePercentiles(st.durations)
		result = append(result, DependencySummary{
			Target:     k.target,
			TargetType: k.targetType,
			CallCount:  st.count,
			ErrorCount: st.errorCount,
			ErrorRate:  errorRate,
			P50Us:      p50,
			P95Us:      p95,
			P99Us:      p99,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CallCount > result[j].CallCount
	})

	return result, nil
}

// extractDependencyTarget examines span attributes for dependency targets.
func extractDependencyTarget(attrJSON string) (target, targetType string) {
	if attrJSON == "" || attrJSON == "{}" {
		return "", ""
	}

	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrJSON), &attrs); err != nil {
		return "", ""
	}

	// Check in priority order — most specific first.

	// Database systems.
	if v, ok := getStringAttr(attrs, "db.system"); ok {
		return v, "database"
	}
	if v, ok := getStringAttr(attrs, "db.name"); ok {
		return v, "database"
	}

	// Peer service (OTel semantic convention for the logical remote service).
	if v, ok := getStringAttr(attrs, "peer.service"); ok {
		return v, "service"
	}

	// RPC services.
	if v, ok := getStringAttr(attrs, "rpc.service"); ok {
		return v, "rpc"
	}

	// Messaging systems.
	if v, ok := getStringAttr(attrs, "messaging.system"); ok {
		return v, "messaging"
	}

	// Cloud provider services.
	if v, ok := getStringAttr(attrs, "aws.service"); ok {
		return v, "aws"
	}

	// HTTP targets — try multiple attribute names.
	if v, ok := getStringAttr(attrs, "http.url"); ok {
		if host := extractHost(v); host != "" {
			return host, "http"
		}
	}
	if v, ok := getStringAttr(attrs, "url.full"); ok {
		if host := extractHost(v); host != "" {
			return host, "http"
		}
	}
	if v, ok := getStringAttr(attrs, "http.host"); ok {
		return v, "http"
	}

	// Network-level peer identification (OTel semantic conventions).
	if v, ok := getStringAttr(attrs, "server.address"); ok {
		return v, "network"
	}
	if v, ok := getStringAttr(attrs, "net.peer.name"); ok {
		return v, "network"
	}

	return "", ""
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
		// Try to extract from non-URL string.
		parts := strings.SplitN(rawURL, "/", 4)
		if len(parts) >= 3 {
			return parts[2]
		}
	}
	return host
}

// computePercentiles calculates p50, p95, p99 from a slice of durations.
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
