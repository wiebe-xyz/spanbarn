package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
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

		result := s.summarizePrompts(ctx, groups)
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

type promptSummaryKey struct{ name, model, service string }

// summarizePrompts folds per-project prompt groups into one summary per name,
// model and service. Each project's ok calls are scaled by that project's own
// sample ratio before the merge. The all-projects view asks for project 0,
// which has no ratio of its own, so scaling by it would apply the default
// 1-in-1000 to every project (#402).
func (s *QueryService) summarizePrompts(ctx context.Context, groups []repository.PromptGroup) []PromptSummary {
	rates := map[int64]float64{}
	index := map[promptSummaryKey]int{}
	result := make([]PromptSummary, 0, len(groups))
	var hists []spanhist.Hist
	for _, g := range groups {
		sr, ok := rates[g.ProjectID]
		if !ok {
			sr = s.projectSampleRate(ctx, g.ProjectID)
			rates[g.ProjectID] = sr
		}
		k := promptSummaryKey{g.Name, g.Model, g.Service}
		i, ok := index[k]
		if !ok {
			i = len(result)
			index[k] = i
			result = append(result, PromptSummary{Name: g.Name, Model: g.Model, Service: g.Service})
			hists = append(hists, spanhist.Hist{})
		}
		p := &result[i]
		p.GenAISystem = max(p.GenAISystem, g.GenAISystem)
		p.CallCount += inflateCount(g.Count, g.ErrorCount, sr)
		p.ErrorCount += g.ErrorCount
		p.TotalTimeUs += g.TotalTimeUs
		p.InputTokens += g.InputTokens
		p.OutputTokens += g.OutputTokens
		p.TotalCostUSD += g.CostUSD
		hists[i].Merge(g.Hist)
	}
	for i := range result {
		p := &result[i]
		if p.CallCount > 0 {
			p.ErrorRate = float64(p.ErrorCount) / float64(p.CallCount)
		}
		p.P50Us = hists[i].Percentile(50)
		p.P95Us = hists[i].Percentile(95)
		p.P99Us = hists[i].Percentile(99)
	}
	return result
}
