package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"

	_ "github.com/wiebe-xyz/spanbarn/internal/repository/migrations"
)

func TestListPrompts(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)

	records := []repository.PromptRecord{
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s1", Service: "api",
			Name: "summarize", GenAISystem: "openai", Model: "gpt-4",
			InputTokens: 100, OutputTokens: 50, CostUSD: 0.01,
			DurationUs: 5000, Status: "ok", StartTimeUs: 1000,
		},
		{
			ProjectID: 1, TraceID: "t2", SpanID: "s2", Service: "api",
			Name: "summarize", GenAISystem: "openai", Model: "gpt-4",
			InputTokens: 200, OutputTokens: 80, CostUSD: 0.02,
			DurationUs: 8000, Status: "error", StartTimeUs: 2000,
		},
		{
			ProjectID: 1, TraceID: "t3", SpanID: "s3", Service: "worker",
			Name: "classify", GenAISystem: "anthropic", Model: "claude-3",
			InputTokens: 50, OutputTokens: 10, CostUSD: 0.005,
			DurationUs: 3000, Status: "ok", StartTimeUs: 3000,
		},
	}
	if err := repo.InsertPromptRecords(records); err != nil {
		t.Fatalf("InsertPromptRecords: %v", err)
	}

	from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	result, err := svc.ListPrompts(context.Background(), 0, from, to, "", "")
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 prompt summaries, got %d", len(result))
	}

	// Sorted by TotalCostUSD desc: summarize ($0.03) first.
	if result[0].Name != "summarize" {
		t.Errorf("expected first prompt 'summarize', got %q", result[0].Name)
	}
	if result[0].CallCount != 2 {
		t.Errorf("expected 2 calls, got %d", result[0].CallCount)
	}
	if result[0].ErrorCount != 1 {
		t.Errorf("expected 1 error, got %d", result[0].ErrorCount)
	}
	if result[0].InputTokens != 300 {
		t.Errorf("expected 300 input tokens, got %d", result[0].InputTokens)
	}

	if result[1].Name != "classify" {
		t.Errorf("expected second prompt 'classify', got %q", result[1].Name)
	}
}

func TestListPromptsServiceFilter(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)

	records := []repository.PromptRecord{
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s1", Service: "api",
			Name: "summarize", Model: "gpt-4", CostUSD: 0.01,
			DurationUs: 5000, Status: "ok", StartTimeUs: 1000,
		},
		{
			ProjectID: 1, TraceID: "t2", SpanID: "s2", Service: "worker",
			Name: "classify", Model: "claude-3", CostUSD: 0.005,
			DurationUs: 3000, Status: "ok", StartTimeUs: 2000,
		},
	}
	if err := repo.InsertPromptRecords(records); err != nil {
		t.Fatalf("InsertPromptRecords: %v", err)
	}

	from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	result, err := svc.ListPrompts(context.Background(), 0, from, to, "api", "")
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 prompt with service filter, got %d", len(result))
	}
	if result[0].Name != "summarize" {
		t.Errorf("expected 'summarize', got %q", result[0].Name)
	}
}

func TestGetPromptDetail(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)

	records := []repository.PromptRecord{
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s1", Service: "api",
			Name: "summarize", Model: "gpt-4", CostUSD: 0.01,
			DurationUs: 5000, Status: "ok", StartTimeUs: 1000,
		},
		{
			ProjectID: 1, TraceID: "t2", SpanID: "s2", Service: "api",
			Name: "classify", Model: "gpt-4", CostUSD: 0.02,
			DurationUs: 8000, Status: "ok", StartTimeUs: 2000,
		},
	}
	if err := repo.InsertPromptRecords(records); err != nil {
		t.Fatalf("InsertPromptRecords: %v", err)
	}

	from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	result, err := svc.GetPromptDetail(context.Background(), 0, from, to, "summarize", "gpt-4", "", "", "")
	if err != nil {
		t.Fatalf("GetPromptDetail: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 record, got %d", len(result))
	}
	if result[0].Name != "summarize" {
		t.Errorf("expected 'summarize', got %q", result[0].Name)
	}
}

// projectRatioLookup returns a 1-in-N ratio per project, and the default
// 1-in-1000 for any project it does not list, as the settings lookup does.
type projectRatioLookup map[int64]int

func (l projectRatioLookup) Ratio(_ context.Context, projectID int64, _ string) int {
	if r, ok := l[projectID]; ok {
		return r
	}
	return 1000
}

// The all-projects view must scale each project's ok calls by that project's
// own ratio. Project 0 has no ratio, so scaling by it multiplied every
// project's calls by the default 1000 (#402).
func TestListPromptsScalesEachProjectByItsOwnRatio(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, projectRatioLookup{1: 1, 2: 10})

	rec := func(project int64, id, status string, durationUs int64) repository.PromptRecord {
		return repository.PromptRecord{
			ProjectID: project, TraceID: "t" + id, SpanID: "s" + id, Service: "api",
			Name: "summarize", Model: "gpt-4", InputTokens: 10, CostUSD: 0.01,
			DurationUs: durationUs, Status: status, StartTimeUs: 1000,
		}
	}
	if err := repo.InsertPromptRecords([]repository.PromptRecord{
		rec(1, "1", "ok", 1000), rec(1, "2", "ok", 2000), rec(1, "3", "error", 3000),
		rec(2, "4", "ok", 400000),
	}); err != nil {
		t.Fatalf("InsertPromptRecords: %v", err)
	}
	from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	all, err := svc.ListPrompts(context.Background(), 0, from, to, "", "")
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want the two projects' groups merged into one summary, got %+v", all)
	}
	got := all[0]
	// Project 1: 2 ok + 1 error at 1-in-1. Project 2: 1 ok at 1-in-10.
	if got.CallCount != 13 || got.ErrorCount != 1 || got.InputTokens != 40 {
		t.Fatalf("calls=%d errors=%d input=%d, want 13, 1, 40", got.CallCount, got.ErrorCount, got.InputTokens)
	}
	if got.P99Us < 300000 {
		t.Errorf("p99 = %d, want the merged histogram to include project 2's 400ms call", got.P99Us)
	}

	one, err := svc.ListPrompts(context.Background(), 2, from, to, "", "")
	if err != nil || len(one) != 1 || one[0].CallCount != 10 {
		t.Fatalf("project 2 alone: want 10 calls, got %+v, %v", one, err)
	}
}
