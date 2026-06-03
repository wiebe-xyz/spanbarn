package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func (s *QueryService) ListPrompts(ctx context.Context, projectID int64, from, to time.Time, svcFilter, modelFilter string) ([]PromptSummary, error) {
	_, span := tracer.Start(ctx, "query.list_prompts")
	defer span.End()

	cacheKey := fmt.Sprintf("prompts:%d:%s:%s:%d:%d", projectID, svcFilter, modelFilter, from.Truncate(time.Minute).Unix(), to.Truncate(time.Minute).Unix())
	if cached, ok := cache.Get[[]PromptSummary](s.cache, ctx, cacheKey); ok {
		return cached, nil
	}

	f := repository.PromptFilter{
		ProjectID: projectID,
		Service:   svcFilter,
		Model:     modelFilter,
		From:      from,
		To:        to,
		Limit:     10000,
	}

	records, err := s.repo.QueryPromptRecords(f)
	if err != nil {
		return nil, err
	}

	type promptStats struct {
		genAISystem string
		model       string
		service     string
		count       int64
		errorCount  int64
		totalTime   int64
		inputToks   int64
		outputToks  int64
		totalCost   float64
		durations   []int64
	}
	byKey := make(map[string]*promptStats)

	for _, rec := range records {
		key := rec.Name + "|" + rec.Model + "|" + rec.Service
		st, ok := byKey[key]
		if !ok {
			st = &promptStats{
				genAISystem: rec.GenAISystem,
				model:       rec.Model,
				service:     rec.Service,
			}
			byKey[key] = st
		}
		st.count++
		if rec.Status == "error" {
			st.errorCount++
		}
		st.totalTime += rec.DurationUs
		st.inputToks += rec.InputTokens
		st.outputToks += rec.OutputTokens
		st.totalCost += rec.CostUSD
		st.durations = append(st.durations, rec.DurationUs)
	}

	sr := s.projectSampleRate(ctx, projectID)
	result := make([]PromptSummary, 0, len(byKey))
	for key, st := range byKey {
		name := key[:len(key)-len("|"+st.model+"|"+st.service)]
		effective := inflateCount(st.count, st.errorCount, sr)
		var errorRate float64
		if effective > 0 {
			errorRate = float64(st.errorCount) / float64(effective)
		}
		p50, p95, p99 := computePercentiles(st.durations)
		result = append(result, PromptSummary{
			Name:         name,
			GenAISystem:  st.genAISystem,
			Model:        st.model,
			Service:      st.service,
			CallCount:    effective,
			ErrorCount:   st.errorCount,
			ErrorRate:    errorRate,
			P50Us:        p50,
			P95Us:        p95,
			P99Us:        p99,
			TotalTimeUs:  st.totalTime,
			InputTokens:  st.inputToks,
			OutputTokens: st.outputToks,
			TotalCostUSD: st.totalCost,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].TotalCostUSD > result[j].TotalCostUSD
	})

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
}

func (s *QueryService) GetPromptDetail(ctx context.Context, projectID int64, from, to time.Time, name, model, svcFilter, status, finishReason string) ([]repository.PromptRecord, error) {
	_, span := tracer.Start(ctx, "query.get_prompt_detail")
	defer span.End()

	f := repository.PromptFilter{
		ProjectID:    projectID,
		Service:      svcFilter,
		Model:        model,
		Status:       status,
		FinishReason: finishReason,
		From:         from,
		To:           to,
		Limit:        500,
	}

	records, err := s.repo.QueryPromptRecords(f)
	if err != nil {
		return nil, err
	}

	var filtered []repository.PromptRecord
	for _, rec := range records {
		if rec.Name == name {
			filtered = append(filtered, rec)
		}
	}
	return filtered, nil
}
