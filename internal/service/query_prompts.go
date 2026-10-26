package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func (s *QueryService) ListPrompts(ctx context.Context, projectID int64, from, to time.Time, svcFilter, modelFilter string) ([]PromptSummary, error) {
	_, span := tracer.Start(ctx, "query.list_prompts")
	defer span.End()

	from, to, grain := snapRange(from, to)
	cacheKey := fmt.Sprintf("prompts:%d:%s:%s:%d:%d", projectID, svcFilter, modelFilter, from.Unix(), to.Unix())
	return readCached(ctx, s.cache, cacheKey, grain, func(ctx context.Context) ([]PromptSummary, error) {

		groups, err := s.repo.QueryPromptGroups(ctx, repository.PromptFilter{
			ProjectID: projectID,
			Service:   svcFilter,
			Model:     modelFilter,
			From:      from,
			To:        to,
		})
		if err != nil {
			return nil, err
		}

		sr := s.projectSampleRate(ctx, projectID)
		result := make([]PromptSummary, 0, len(groups))
		for _, g := range groups {
			effective := inflateCount(g.Count, g.ErrorCount, sr)
			var errorRate float64
			if effective > 0 {
				errorRate = float64(g.ErrorCount) / float64(effective)
			}
			result = append(result, PromptSummary{
				Name:         g.Name,
				GenAISystem:  g.GenAISystem,
				Model:        g.Model,
				Service:      g.Service,
				CallCount:    effective,
				ErrorCount:   g.ErrorCount,
				ErrorRate:    errorRate,
				P50Us:        g.Hist.Percentile(50),
				P95Us:        g.Hist.Percentile(95),
				P99Us:        g.Hist.Percentile(99),
				TotalTimeUs:  g.TotalTimeUs,
				InputTokens:  g.InputTokens,
				OutputTokens: g.OutputTokens,
				TotalCostUSD: g.CostUSD,
			})
		}

		sort.Slice(result, func(i, j int) bool {
			return result[i].TotalCostUSD > result[j].TotalCostUSD
		})

		return result, nil
	})
}

func (s *QueryService) GetPromptDetail(ctx context.Context, projectID int64, from, to time.Time, name, model, svcFilter, status, finishReason string) ([]repository.PromptRecord, error) {
	_, span := tracer.Start(ctx, "query.get_prompt_detail")
	defer span.End()

	return s.repo.QueryPromptRecords(repository.PromptFilter{
		ProjectID:    projectID,
		Service:      svcFilter,
		Name:         name,
		Model:        model,
		Status:       status,
		FinishReason: finishReason,
		From:         from,
		To:           to,
		Limit:        500,
	})
}
