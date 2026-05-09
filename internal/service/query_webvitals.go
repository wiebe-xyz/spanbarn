package service

import (
	"context"
	"sort"
	"time"
)

type WebVitalSummary struct {
	Page    string `json:"page"`
	Metric  string `json:"metric"`
	P50Ms   float64 `json:"p50Ms"`
	P95Ms   float64 `json:"p95Ms"`
	Samples int     `json:"samples"`
	Good    int     `json:"good"`
	NeedsImprovement int `json:"needsImprovement"`
	Poor    int     `json:"poor"`
}

func (s *QueryService) GetWebVitals(ctx context.Context, from, to time.Time) ([]WebVitalSummary, error) {
	_, span := tracer.Start(ctx, "query.web_vitals")
	defer span.End()

	rows, err := s.repo.QueryWebVitals(from, to)
	if err != nil {
		return nil, err
	}

	type key struct{ page, metric string }
	type bucket struct {
		values []float64
		good, ni, poor int
	}

	byKey := make(map[key]*bucket)
	for _, r := range rows {
		k := key{r.Page, r.Metric}
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
		if result[i].Page != result[j].Page {
			return result[i].Page < result[j].Page
		}
		return result[i].Metric < result[j].Metric
	})

	return result, nil
}
