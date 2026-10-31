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

		result := summarizePrompts(groups)
		sort.Slice(result, func(i, j int) bool {
			return result[i].TotalCostUSD > result[j].TotalCostUSD
		})
		s.attachWindowLoss(ctx, result, projectID, from, to)

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
// model and service. CallCount sums the stored sample weights, so a trace kept
// by the error or slow rule counts once and only a ratio-sampled trace counts
// for more. Scaling every ok call by the project's ratio read 2 calls per record
// on a 1-in-2 project whose traces were all kept (#471).
func summarizePrompts(groups []repository.PromptGroup) []PromptSummary {
	index := map[promptSummaryKey]int{}
	result := make([]PromptSummary, 0, len(groups))
	var hists []spanhist.Hist
	for _, g := range groups {
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
		p.CallCount += g.WeightedCount
		p.StoredCount += g.Count
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
