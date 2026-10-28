package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// spanFallbackWindow caps how far back we ever scan the raw `spans` table when
// merging unaggregated tail data into a query result. Since inline aggregation
// upserts on every ingest batch (commit 5a2e173), anything older than this
// window is already in the `aggregates` table — scanning further is pure cost
// and was the cause of the "context deadline exceeded" issues (SPA-13/14/17).
const spanFallbackWindow = 90 * time.Second

// aggStats accumulates count, error count and count-weighted percentile sums so
// a weighted p50/p95/p99 can be derived. ListServices, ListOperations and
// GetTimeseries build the same rollup, keyed by service / operation / bucket.
type aggStats struct {
	count, errorCount               int64
	aggP50Sum, aggP95Sum, aggP99Sum int64
	aggCount                        int64
	// effective is the estimated true span population: each folded row scaled
	// by the sample rate of its own operation.
	effective int64
}

// foldAggregate folds a pre-aggregated row in, estimated by estimateCount; its
// percentiles are always meaningful, so they are always count-weighted into the
// sums.
func (a *aggStats) foldAggregate(rate float64, row repository.Aggregate) {
	a.effective += estimateCount(row, rate)
	a.count += row.Count
	a.errorCount += row.ErrorCount
	a.aggP50Sum += row.P50Us * row.Count
	a.aggP95Sum += row.P95Us * row.Count
	a.aggP99Sum += row.P99Us * row.Count
	a.aggCount += row.Count
}

// foldSpanFallback folds a raw-span-derived row in, estimated by
// estimateSpans. Such rows may carry no percentile data, so the percentile sums
// are only weighted in when present.
func (a *aggStats) foldSpanFallback(rate float64, count, errorCount int64, w repository.SpanWeights, p50, p95, p99 int64) {
	a.effective += estimateSpans(count, errorCount, w, rate)
	a.count += count
	a.errorCount += errorCount
	if p50 > 0 || p95 > 0 || p99 > 0 {
		a.aggP50Sum += p50 * count
		a.aggP95Sum += p95 * count
		a.aggP99Sum += p99 * count
		a.aggCount += count
	}
}

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

	from, to, grain := snapRange(from, to)
	cacheKey := fmt.Sprintf("services:%d:%d:%d:%v", projectID, from.Unix(), to.Unix(), serverOnly)
	return readCached(ctx, s.cache, cacheKey, grain, func(ctx context.Context) ([]ServiceSummary, error) {
		return s.listServicesUncached(ctx, projectID, from, to, serverOnly)
	})
}

func (s *QueryService) listServicesUncached(ctx context.Context, projectID int64, from, to time.Time, serverOnly bool) ([]ServiceSummary, error) {
	kind := ""
	if serverOnly {
		kind = "server"
	}
	if out, ok, err := s.servicesFromRollups(ctx, projectID, from, to, kind); ok {
		return out, err
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

	rates := s.newOpRates(ctx, projectID)
	merged := make(map[string]*aggStats)
	for _, a := range aggs {
		statsFor(merged, a.Service).foldAggregate(rates.rate(a.Operation), a)
	}

	// Merge recent in-memory aggregates (or fall back to raw spans when no
	// accumulator is wired — reader pods and standalone mode).
	if fbFrom, ok := narrowFallback(from, to); ok {
		s.foldRecentServiceStats(merged, rates, projectID, kind, fbFrom, to)
	}

	return serviceSummaries(merged), nil
}

// statsFor returns the stats entry for key, creating it when missing.
func statsFor(m map[string]*aggStats, key string) *aggStats {
	st := m[key]
	if st == nil {
		st = &aggStats{}
		m[key] = st
	}
	return st
}

// foldRecentServiceStats adds the not yet persisted window into merged, from
// the in-memory accumulator or, without one, from the raw spans.
func (s *QueryService) foldRecentServiceStats(merged map[string]*aggStats, rates *opRates, projectID int64, kind string, from, to time.Time) {
	if s.accumulator != nil {
		for _, a := range s.accumulator.QueryRecent(repository.AggregateFilter{
			ProjectID: projectID, Kind: kind, From: from, To: to,
		}) {
			statsFor(merged, a.Service).foldAggregate(rates.rate(a.Operation), a)
		}
		return
	}
	spanStats, err := s.repo.QueryServiceStatsFromSpans(projectID, from, to, kind)
	if err != nil {
		s.logger.Warn("failed to query service stats from spans", "error", err)
	}
	for _, ss := range spanStats {
		statsFor(merged, ss.Service).foldSpanFallback(rates.rate(""), ss.Count, ss.ErrorCount, ss.Weights, ss.P50Us, ss.P95Us, ss.P99Us)
	}
}

// serviceSummaries turns the per-service stats into summaries, using the
// per-operation weighted span population and ordering by span count.
func serviceSummaries(merged map[string]*aggStats) []ServiceSummary {
	result := make([]ServiceSummary, 0, len(merged))
	for svc, ms := range merged {
		effective := ms.effective
		var errorRate float64
		if effective > 0 {
			errorRate = float64(ms.errorCount) / float64(effective)
		}

		var p50, p95, p99 int64
		if ms.aggCount > 0 {
			p50 = ms.aggP50Sum / ms.aggCount
			p95 = ms.aggP95Sum / ms.aggCount
			p99 = ms.aggP99Sum / ms.aggCount
		}

		result = append(result, ServiceSummary{
			Service:    svc,
			SpanCount:  effective,
			ErrorCount: ms.errorCount,
			ErrorRate:  errorRate,
			P50Us:      p50,
			P95Us:      p95,
			P99Us:      p99,
			Source:     SourceAggregates,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].SpanCount > result[j].SpanCount
	})
	return result
}

// ListOperations returns aggregated metrics per operation for a service.
// kind filters by span kind (e.g. "server" for entry points only); empty means all.
func (s *QueryService) ListOperations(ctx context.Context, projectID int64, service string, from, to time.Time, kind string) ([]OperationSummary, error) {
	_, span := tracer.Start(ctx, "query.list_operations")
	span.SetAttributes(attribute.String("service", service))
	defer span.End()

	from, to, grain := snapRange(from, to)
	cacheKey := fmt.Sprintf("ops:%d:%s:%s:%d:%d", projectID, service, kind, from.Unix(), to.Unix())
	return readCached(ctx, s.cache, cacheKey, grain, func(ctx context.Context) ([]OperationSummary, error) {

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
		rates := s.newOpRates(ctx, projectID)
		byOp := make(map[opKey]*aggStats)
		for _, a := range aggs {
			k := opKey{a.Operation, a.Resource, a.Kind}
			st, ok := byOp[k]
			if !ok {
				st = &aggStats{}
				byOp[k] = st
			}
			st.foldAggregate(rates.rate(a.Operation), a)
		}

		if fbFrom, ok := narrowFallback(from, to); ok {
			if s.accumulator != nil {
				for _, a := range s.accumulator.QueryRecent(repository.AggregateFilter{
					ProjectID: projectID, Service: service, Kind: kind, From: fbFrom, To: to,
				}) {
					k := opKey{CollapseParamLists(a.Operation), CollapseParamLists(a.Resource), a.Kind}
					st := byOp[k]
					if st == nil {
						st = &aggStats{}
						byOp[k] = st
					}
					st.foldAggregate(rates.rate(a.Operation), a)
				}
			} else {
				spanStats, err := s.repo.QueryOperationStatsFromSpans(projectID, service, fbFrom, to, kind)
				if err != nil {
					s.logger.Warn("failed to query operation stats from spans", "error", err)
				}
				for _, ss := range spanStats {
					k := opKey{CollapseParamLists(ss.Operation), CollapseParamLists(ss.Resource), ss.Kind}
					st := byOp[k]
					if st == nil {
						st = &aggStats{}
						byOp[k] = st
					}
					st.foldSpanFallback(rates.rate(ss.Operation), ss.Count, ss.ErrorCount, ss.Weights, ss.P50Us, ss.P95Us, ss.P99Us)
				}
			}
		}

		result := make([]OperationSummary, 0, len(byOp))
		for k, st := range byOp {
			effective := st.effective
			var errorRate float64
			if effective > 0 {
				errorRate = float64(st.errorCount) / float64(effective)
			}
			var p50, p95, p99 int64
			if st.aggCount > 0 {
				p50 = st.aggP50Sum / st.aggCount
				p95 = st.aggP95Sum / st.aggCount
				p99 = st.aggP99Sum / st.aggCount
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

		return result, nil
	})
}

// GetTimeseries returns bucketed timeseries data for a specific operation, or
// for every operation of the service when operation is "*". A zero interval
// picks the dashboard's chart bucket width for the window.
func (s *QueryService) GetTimeseries(ctx context.Context, projectID int64, svcName, operation string, from, to time.Time, interval time.Duration) ([]TimeseriesBucket, error) {
	_, span := tracer.Start(ctx, "query.get_timeseries")
	span.SetAttributes(
		attribute.String("service", svcName),
		attribute.String("operation", operation),
	)
	defer span.End()

	from, to, grain := snapRange(from, to)
	interval = timeseriesInterval(from, to, interval)
	cacheKey := fmt.Sprintf("ts:%d:%s:%s:%d:%d:%d", projectID, svcName, operation, from.Unix(), to.Unix(), int64(interval.Seconds()))
	return readCached(ctx, s.cache, cacheKey, grain, func(ctx context.Context) ([]TimeseriesBucket, error) {
		if out, ok, err := s.timeseriesFromRollups(ctx, projectID, svcName, operation, from, to, interval); ok {
			return out, err
		}
		if operation == allOperations {
			operation = ""
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

		rates := s.newOpRates(ctx, projectID)
		byBucket := make(map[time.Time]*aggStats)
		for _, a := range aggs {
			b := a.Bucket.Truncate(interval)
			st, ok := byBucket[b]
			if !ok {
				st = &aggStats{}
				byBucket[b] = st
			}
			st.foldAggregate(rates.rate(a.Operation), a)
		}

		if fbFrom, ok := narrowFallback(from, to); ok {
			if s.accumulator != nil {
				for _, a := range s.accumulator.QueryRecent(repository.AggregateFilter{
					ProjectID: projectID, Service: svcName, Operation: operation, From: fbFrom, To: to,
				}) {
					b := a.Bucket.Truncate(interval)
					st := byBucket[b]
					if st == nil {
						st = &aggStats{}
						byBucket[b] = st
					}
					st.foldAggregate(rates.rate(a.Operation), a)
				}
			} else {
				spanBuckets, err := s.repo.QuerySpanTimeseries(projectID, svcName, operation, fbFrom, to, int64(interval.Seconds()))
				if err != nil {
					s.logger.Warn("failed to query span timeseries", "error", err)
				}
				for _, sb := range spanBuckets {
					b := sb.Bucket.Truncate(interval)
					st := byBucket[b]
					if st == nil {
						st = &aggStats{}
						byBucket[b] = st
					}
					st.foldSpanFallback(rates.rate(operation), sb.Count, sb.ErrorCount, sb.Weights, sb.P50Us, sb.P95Us, sb.P99Us)
				}
			}
		}

		result := make([]TimeseriesBucket, 0, len(byBucket))
		for b, st := range byBucket {
			var p50, p95, p99 int64
			if st.aggCount > 0 {
				p50 = st.aggP50Sum / st.aggCount
				p95 = st.aggP95Sum / st.aggCount
				p99 = st.aggP99Sum / st.aggCount
			}
			result = append(result, TimeseriesBucket{
				Bucket:     b,
				Count:      st.effective,
				ErrorCount: st.errorCount,
				P50Us:      p50,
				P95Us:      p95,
				P99Us:      p99,
				Source:     SourceAggregates,
			})
		}

		sort.Slice(result, func(i, j int) bool {
			return result[i].Bucket.Before(result[j].Bucket)
		})

		return result, nil
	})
}
