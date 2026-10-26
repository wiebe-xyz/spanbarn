package repository

import (
	"context"
	"math"
	"testing"
	"time"
)

func promptRec(projectID int64, span, name, model string, durUs int64, status string, cost float64) PromptRecord {
	return PromptRecord{
		ProjectID: projectID, TraceID: "t-" + span, SpanID: span, Service: "api",
		Name: name, GenAISystem: "openai", Model: model,
		PromptBody: "prompt " + span, ResponseBody: "response " + span,
		InputTokens: 10, OutputTokens: 5, CostUSD: cost,
		DurationUs: durUs, Status: status, StartTimeUs: time.Now().UnixMicro(),
	}
}

func TestQueryPromptGroupsFoldsEveryPrompt(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	other, _ := repo.CreateProject("other", "Other")

	var recs []PromptRecord
	for i := range 10 {
		status := "ok"
		if i == 0 {
			status = "error"
		}
		recs = append(recs, promptRec(p.ID, "s"+string(rune('a'+i)), "chat", "gpt-4", int64(1000*(i+1)), status, 0.01))
	}
	recs = append(recs,
		promptRec(p.ID, "x1", "chat", "gpt-5", 500, "ok", 0.5),
		promptRec(other.ID, "y1", "chat", "gpt-4", 500, "ok", 1),
	)
	if err := repo.InsertPromptRecords(recs); err != nil {
		t.Fatal(err)
	}

	groups, err := repo.QueryPromptGroups(context.Background(), PromptFilter{ProjectID: p.ID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2 (chat on gpt-4 and gpt-5): %+v", len(groups), groups)
	}
	var g PromptGroup
	for _, x := range groups {
		if x.Model == "gpt-4" {
			g = x
		}
	}
	if g.Count != 10 || g.Hist.Count() != 10 {
		t.Fatalf("gpt-4 group counts %d prompts (hist %d), want 10; Limit must not cut the groups", g.Count, g.Hist.Count())
	}
	if g.ErrorCount != 1 || g.InputTokens != 100 || g.OutputTokens != 50 || g.TotalTimeUs != 55000 {
		t.Fatalf("gpt-4 group sums %+v", g)
	}
	if math.Abs(g.CostUSD-0.1) > 1e-9 || g.GenAISystem != "openai" || g.Service != "api" || g.Name != "chat" {
		t.Fatalf("gpt-4 group fields %+v", g)
	}
	if p99 := g.Hist.Percentile(99); p99 < 9000 || p99 > 10000 {
		t.Fatalf("p99 %d, want within the top bin (9000-10000)", p99)
	}
}

func TestQueryPromptRecordsFiltersByNameBeforeTheLimit(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	if err := repo.InsertPromptRecords([]PromptRecord{promptRec(p.ID, "old", "summarize", "gpt-4", 100, "ok", 0)}); err != nil {
		t.Fatal(err)
	}
	var newer []PromptRecord
	for _, s := range []string{"n1", "n2", "n3"} {
		newer = append(newer, promptRec(p.ID, s, "classify", "gpt-4", 100, "ok", 0))
	}
	if err := repo.InsertPromptRecords(newer); err != nil {
		t.Fatal(err)
	}

	recs, err := repo.QueryPromptRecords(PromptFilter{ProjectID: p.ID, Name: "summarize", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Name != "summarize" {
		t.Fatalf("got %+v, want the one summarize record", recs)
	}
}
