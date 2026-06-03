package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// spanFallbackWindow caps how far back we ever scan the raw `spans` table when
// merging unaggregated tail data into a query result. Since inline aggregation
// upserts on every ingest batch (commit 5a2e173), anything older than this
// window is already in the `aggregates` table — scanning further is pure cost
// and was the cause of the "context deadline exceeded" issues (SPA-13/14/17).
const spanFallbackWindow = 90 * time.Second

// narrowFallback clamps `from` to be no earlier than now-spanFallbackWindow.
// If `to` is older than that window, returns ok=false so the caller can skip
// the raw-span query entirely.
func narrowFallback(from, to time.Time) (time.Time, bool) {
	cutoff := time.Now().UTC().Add(-spanFallbackWindow)
	if !to.IsZero() && to.Before(cutoff) {
		return time.Time{}, false
	}
	if from.IsZero() || from.Before(cutoff) {
		return cutoff, true
	}
	return from, true
}

// ListServices returns aggregated metrics per service for the given time range.
// When serverOnly is true, only server-kind spans are considered, which gives a
// view of each service from the perspective of how it serves requests (entry
// points) rather than its internal or outbound client calls.
func (s *QueryService) ListServices(ctx context.Context, projectID int64, from, to time.Time, serverOnly bool) ([]ServiceSummary, error) {
	_, span := tracer.Start(ctx, "query.list_services")
	defer span.End()

	cacheKey := fmt.Sprintf("services:%d:%d:%d:%v", projectID, from.Truncate(time.Minute).Unix(), to.Truncate(time.Minute).Unix(), serverOnly)
	if cached, ok := cache.Get[[]ServiceSummary](s.cache, ctx, cacheKey); ok {
		return cached, nil
	}

	if stale, found, _ := cache.GetStale[[]ServiceSummary](s.cache, ctx, cacheKey); found {
		if _, already := revalidating.LoadOrStore(cacheKey, true); !already {
			go func() {
				defer revalidating.Delete(cacheKey)
				if result, err := s.listServicesUncached(context.Background(), projectID, from, to, serverOnly); err == nil {
					cache.Set(s.cache, context.Background(), cacheKey, result)
				}
			}()
		}
		return stale, nil
	}

	result, err := s.listServicesUncached(ctx, projectID, from, to, serverOnly)
	if err != nil {
		return nil, err
	}

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
}

func (s *QueryService) listServicesUncached(ctx context.Context, projectID int64, from, to time.Time, serverOnly bool) ([]ServiceSummary, error) {
	kind := ""
	if serverOnly {
		kind = "server"
	}
	aggs, err := s.repo.QueryAggregates(repository.AggregateFilter{
		ProjectID: projectID,
		Kind:      kind,
		From:      from,
		To:        to,
		Limit:     10000,
	})
	if err != nil {
		return nil, err
	}

	type svcStats struct {
		count, errorCount, p50Sum, p95Sum, p99Sum, buckets int64
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

	var spanStats []repository.ServiceStats
	if fbFrom, ok := narrowFallback(from, to); ok {
		var err error
		spanStats, err = s.repo.QueryServiceStatsFromSpans(projectID, fbFrom, to, kind)
		if err != nil {
			s.logger.Warn("failed to query service stats from spans", "error", err)
		}
	}

	type mergedStats struct {
		count, errorCount              int64
		aggP50Sum, aggP95Sum, aggP99Sum int64
		aggCount                       int64
		spanP50, spanP95, spanP99      int64
		hasSpanPercentiles             bool
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
		if ss.P50Us > 0 || ss.P95Us > 0 || ss.P99Us > 0 {
			ms.spanP50 = ss.P50Us
			ms.spanP95 = ss.P95Us
			ms.spanP99 = ss.P99Us
			ms.hasSpanPercentiles = true
		}
	}

	result := make([]ServiceSummary, 0, len(merged))
	for svc, ms := range merged {
		effective := inflateCount(ms.count, ms.errorCount, s.sampleRate)
		var errorRate float64
		if effective > 0 {
			errorRate = float64(ms.errorCount) / float64(effective)
		}

		var p50, p95, p99 int64
		if ms.aggCount > 0 {
			p50 = ms.aggP50Sum / ms.aggCount
			p95 = ms.aggP95Sum / ms.aggCount
			p99 = ms.aggP99Sum / ms.aggCount
		} else if ms.hasSpanPercentiles {
			p50 = ms.spanP50
			p95 = ms.spanP95
			p99 = ms.spanP99
		}

		result = append(result, ServiceSummary{
			Service:    svc,
			SpanCount:  effective,
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
// kind filters by span kind (e.g. "server" for entry points only); empty means all.
func (s *QueryService) ListOperations(ctx context.Context, projectID int64, service string, from, to time.Time, kind string) ([]OperationSummary, error) {
	_, span := tracer.Start(ctx, "query.list_operations")
	span.SetAttributes(attribute.String("service", service))
	defer span.End()

	cacheKey := fmt.Sprintf("ops:%d:%s:%s:%d:%d", projectID, service, kind, from.Truncate(time.Minute).Unix(), to.Truncate(time.Minute).Unix())
	if cached, ok := cache.Get[[]OperationSummary](s.cache, ctx, cacheKey); ok {
		return cached, nil
	}

	aggs, err := s.repo.QueryAggregates(repository.AggregateFilter{
		ProjectID: projectID,
		Service:   service,
		Kind:      kind,
		From:      from,
		To:        to,
		Limit:     10000,
	})
	if err != nil {
		return nil, err
	}

	type opKey struct {
		operation, resource, kind string
	}
	type opStats struct {
		count, errorCount              int64
		aggP50Sum, aggP95Sum, aggP99Sum int64
		aggCount                       int64
		spanP50, spanP95, spanP99      int64
		hasSpanPercentiles             bool
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
		st.aggP50Sum += a.P50Us * a.Count
		st.aggP95Sum += a.P95Us * a.Count
		st.aggP99Sum += a.P99Us * a.Count
		st.aggCount += a.Count
	}

	var spanStats []repository.OperationStats
	if fbFrom, ok := narrowFallback(from, to); ok {
		var err error
		spanStats, err = s.repo.QueryOperationStatsFromSpans(projectID, service, fbFrom, to, kind)
		if err != nil {
			s.logger.Warn("failed to query operation stats from spans", "error", err)
		}
	}
	for _, ss := range spanStats {
		k := opKey{ss.Operation, ss.Resource, ss.Kind}
		st, ok := byOp[k]
		if !ok {
			st = &opStats{}
			byOp[k] = st
		}
		st.count += ss.Count
		st.errorCount += ss.ErrorCount
		if ss.P50Us > 0 || ss.P95Us > 0 || ss.P99Us > 0 {
			st.spanP50 = ss.P50Us
			st.spanP95 = ss.P95Us
			st.spanP99 = ss.P99Us
			st.hasSpanPercentiles = true
		}
	}

	result := make([]OperationSummary, 0, len(byOp))
	for k, st := range byOp {
		effective := inflateCount(st.count, st.errorCount, s.sampleRate)
		var errorRate float64
		if effective > 0 {
			errorRate = float64(st.errorCount) / float64(effective)
		}
		var p50, p95, p99 int64
		if st.aggCount > 0 {
			p50 = st.aggP50Sum / st.aggCount
			p95 = st.aggP95Sum / st.aggCount
			p99 = st.aggP99Sum / st.aggCount
		} else if st.hasSpanPercentiles {
			p50 = st.spanP50
			p95 = st.spanP95
			p99 = st.spanP99
		}
		result = append(result, OperationSummary{
			Operation:  k.operation,
			Resource:   k.resource,
			Kind:       k.kind,
			SpanCount:  effective,
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

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
}

// GetTimeseries returns bucketed timeseries data for a specific operation.
func (s *QueryService) GetTimeseries(ctx context.Context, projectID int64, svcName, operation string, from, to time.Time, interval time.Duration) ([]TimeseriesBucket, error) {
	_, span := tracer.Start(ctx, "query.get_timeseries")
	span.SetAttributes(
		attribute.String("service", svcName),
		attribute.String("operation", operation),
	)
	defer span.End()

	cacheKey := fmt.Sprintf("ts:%d:%s:%s:%d:%d:%d", projectID, svcName, operation, from.Truncate(time.Minute).Unix(), to.Truncate(time.Minute).Unix(), int64(interval.Seconds()))
	if cached, ok := cache.Get[[]TimeseriesBucket](s.cache, ctx, cacheKey); ok {
		return cached, nil
	}

	aggs, err := s.repo.QueryAggregates(repository.AggregateFilter{
		ProjectID: projectID,
		Service:   svcName,
		Operation: operation,
		From:      from,
		To:        to,
		Limit:     100000,
	})
	if err != nil {
		return nil, err
	}

	type bucketStats struct {
		count, errorCount              int64
		aggP50Sum, aggP95Sum, aggP99Sum int64
		aggCount                       int64
		spanP50, spanP95, spanP99      int64
		hasSpanPercentiles             bool
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
		st.aggP50Sum += a.P50Us * a.Count
		st.aggP95Sum += a.P95Us * a.Count
		st.aggP99Sum += a.P99Us * a.Count
		st.aggCount += a.Count
	}

	var spanBuckets []repository.SpanBucket
	if fbFrom, ok := narrowFallback(from, to); ok {
		var err error
		spanBuckets, err = s.repo.QuerySpanTimeseries(projectID, svcName, operation, fbFrom, to, int64(interval.Seconds()))
		if err != nil {
			s.logger.Warn("failed to query span timeseries", "error", err)
		}
	}
	for _, sb := range spanBuckets {
		b := sb.Bucket.Truncate(interval)
		st, ok := byBucket[b]
		if !ok {
			st = &bucketStats{}
			byBucket[b] = st
		}
		st.count += sb.Count
		st.errorCount += sb.ErrorCount
		if sb.P50Us > 0 || sb.P95Us > 0 || sb.P99Us > 0 {
			st.spanP50 = sb.P50Us
			st.spanP95 = sb.P95Us
			st.spanP99 = sb.P99Us
			st.hasSpanPercentiles = true
		}
	}

	result := make([]TimeseriesBucket, 0, len(byBucket))
	for b, st := range byBucket {
		var p50, p95, p99 int64
		if st.aggCount > 0 {
			p50 = st.aggP50Sum / st.aggCount
			p95 = st.aggP95Sum / st.aggCount
			p99 = st.aggP99Sum / st.aggCount
		} else if st.hasSpanPercentiles {
			p50 = st.spanP50
			p95 = st.spanP95
			p99 = st.spanP99
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

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
}
