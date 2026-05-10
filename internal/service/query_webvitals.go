package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
)

type WebVitalSummary struct {
	Service string `json:"service"`
	Page    string `json:"page"`
	Metric  string `json:"metric"`
	P50Ms   float64 `json:"p50Ms"`
	P95Ms   float64 `json:"p95Ms"`
	Samples int     `json:"samples"`
	Good    int     `json:"good"`
	NeedsImprovement int `json:"needsImprovement"`
	Poor    int     `json:"poor"`
}

var revalidating sync.Map

func (s *QueryService) GetWebVitals(ctx context.Context, service string, from, to time.Time) ([]WebVitalSummary, error) {
	_, span := tracer.Start(ctx, "query.web_vitals")
	defer span.End()

	cacheKey := fmt.Sprintf("vitals:%s:%d:%d", service, from.Truncate(time.Minute).Unix(), to.Truncate(time.Minute).Unix())
	if cached, ok := cache.Get[[]WebVitalSummary](s.cache, ctx, cacheKey); ok {
		return cached, nil
	}

	if stale, found, _ := cache.GetStale[[]WebVitalSummary](s.cache, ctx, cacheKey); found {
		if _, already := revalidating.LoadOrStore(cacheKey, true); !already {
			go func() {
				defer revalidating.Delete(cacheKey)
				if result, err := s.fetchWebVitals(context.Background(), service, from, to); err == nil {
					cache.Set(s.cache, context.Background(), cacheKey, result)
				}
			}()
		}
		return stale, nil
	}

	result, err := s.fetchWebVitals(ctx, service, from, to)
	if err != nil {
		return nil, err
	}

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
}

func (s *QueryService) fetchWebVitals(ctx context.Context, service string, from, to time.Time) ([]WebVitalSummary, error) {
	rows, err := s.repo.QueryWebVitals(service, from, to)
	if err != nil {
		return nil, err
	}

	type key struct{ service, page, metric string }
	type bucket struct {
		values []float64
		good, ni, poor int
	}

	byKey := make(map[key]*bucket)
	for _, r := range rows {
		k := key{r.Service, r.Page, r.Metric}
		b, ok := byKey[k]
		if !ok {
			b = &bucket{}
			byKey[k] = b
		}
		valueMs := float64(r.ValueUs) / 1000.0
		b.values = append(b.values, valueMs)
		switch r.Rating {
		case "good":
			b.good++
		case "needs-improvement":
			b.ni++
		default:
			b.poor++
		}
	}

	result := make([]WebVitalSummary, 0, len(byKey))
	for k, b := range byKey {
		sort.Float64s(b.values)
		n := len(b.values)
		p50 := b.values[n*50/100]
		p95Idx := n * 95 / 100
		if p95Idx >= n {
			p95Idx = n - 1
		}
		p95 := b.values[p95Idx]

		result = append(result, WebVitalSummary{
			Service: k.service,
			Page:    k.page,
			Metric:  k.metric,
			P50Ms:   p50,
			P95Ms:   p95,
			Samples: n,
			Good:    b.good,
			NeedsImprovement: b.ni,
			Poor:    b.poor,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Service != result[j].Service {
			return result[i].Service < result[j].Service
		}
		if result[i].Page != result[j].Page {
			return result[i].Page < result[j].Page
		}
		return result[i].Metric < result[j].Metric
	})

	return result, nil
}

// WebVitalTimeseriesBucket holds metrics for one time bucket of a web vital.
type WebVitalTimeseriesBucket struct {
	Bucket  time.Time `json:"bucket"`
	P50Ms   float64   `json:"p50Ms"`
	P95Ms   float64   `json:"p95Ms"`
	Samples int64     `json:"samples"`
	Good    int64     `json:"good"`
	NI      int64     `json:"needsImprovement"`
	Poor    int64     `json:"poor"`
}

func (s *QueryService) GetWebVitalsTimeseries(ctx context.Context, service, page, metric string, from, to time.Time, interval time.Duration) ([]WebVitalTimeseriesBucket, error) {
	_, span := tracer.Start(ctx, "query.web_vitals_timeseries")
	defer span.End()

	cacheKey := fmt.Sprintf("vitals-ts:%s:%s:%s:%d:%d:%d", service, page, metric, from.Truncate(time.Minute).Unix(), to.Truncate(time.Minute).Unix(), int64(interval.Seconds()))
	if cached, ok := cache.Get[[]WebVitalTimeseriesBucket](s.cache, ctx, cacheKey); ok {
		return cached, nil
	}

	if stale, found, _ := cache.GetStale[[]WebVitalTimeseriesBucket](s.cache, ctx, cacheKey); found {
		if _, already := revalidating.LoadOrStore(cacheKey, true); !already {
			go func() {
				defer revalidating.Delete(cacheKey)
				if result, err := s.fetchWebVitalsTimeseries(context.Background(), service, page, metric, from, to, interval); err == nil {
					cache.Set(s.cache, context.Background(), cacheKey, result)
				}
			}()
		}
		return stale, nil
	}

	result, err := s.fetchWebVitalsTimeseries(ctx, service, page, metric, from, to, interval)
	if err != nil {
		return nil, err
	}

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
}

func (s *QueryService) fetchWebVitalsTimeseries(ctx context.Context, service, page, metric string, from, to time.Time, interval time.Duration) ([]WebVitalTimeseriesBucket, error) {
	buckets, err := s.repo.QueryWebVitalsTimeseries(service, page, metric, from, to, int64(interval.Seconds()))
	if err != nil {
		return nil, err
	}

	result := make([]WebVitalTimeseriesBucket, 0, len(buckets))
	for _, b := range buckets {
		result = append(result, WebVitalTimeseriesBucket{
			Bucket:  b.Bucket,
			P50Ms:   float64(b.P50Us) / 1000.0,
			P95Ms:   float64(b.P95Us) / 1000.0,
			Samples: b.Samples,
			Good:    b.Good,
			NI:      b.NI,
			Poor:    b.Poor,
		})
	}

	return result, nil
}
