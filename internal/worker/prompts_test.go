package worker

import (
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestExtractPromptRecordsFields(t *testing.T) {
	spans := []repository.Span{
		{Name: "not genai", Attributes: `{"foo":"bar"}`},
		{Name: "empty", Attributes: `{}`},
		{
			ProjectID: 3, TraceID: "t", SpanID: "s", Name: "chat", Service: "svc",
			DurationUs: 42, Status: "ok", StartTimeUs: 7,
			Attributes: `{
				"gen_ai.system":"openai",
				"gen_ai.request.model":"req-model",
				"gen_ai.request.temperature":0.5,
				"gen_ai.request.max_tokens":128,
				"gen_ai.usage.input_tokens":10,
				"gen_ai.usage.output_tokens":5,
				"gen_ai.usage.cost":0.25,
				"gen_ai.quality_score":0.9,
				"gen_ai.prompt":"attr prompt",
				"span.output":"attr output",
				"feature_flag.key":"flag"
			}`,
			Events: "[]",
		},
	}
	got := extractPromptRecords(spans)
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	r := got[0]
	if r.Model != "req-model" || r.GenAISystem != "openai" || r.ProjectID != 3 {
		t.Errorf("identity fields wrong: %+v", r)
	}
	if r.Temperature == nil || *r.Temperature != 0.5 || r.MaxTokens == nil || *r.MaxTokens != 128 {
		t.Errorf("request params wrong: %+v", r)
	}
	if r.QualityScore == nil || *r.QualityScore != 0.9 {
		t.Errorf("quality score wrong")
	}
	if r.TotalTokens != 15 || r.CostUSD != 0.25 {
		t.Errorf("usage wrong: total=%d cost=%v", r.TotalTokens, r.CostUSD)
	}
	if r.PromptBody != "attr prompt" || r.ResponseBody != "attr output" {
		t.Errorf("bodies = %q / %q", r.PromptBody, r.ResponseBody)
	}
	if r.PromptHash != hashString("chat") || r.FeatureFlagKey != "flag" {
		t.Errorf("hash/flag wrong: %+v", r)
	}
}

func TestFirstStrAttrPrefersEarlierKey(t *testing.T) {
	attrs := map[string]any{"a": "", "b": "second", "c": "third"}
	if got := firstStrAttr(attrs, "a", "b", "c"); got != "second" {
		t.Errorf("got %q", got)
	}
	if got := firstStrAttr(attrs, "x"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestStampBoringExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	ingested := time.Unix(500, 0)
	spans := []repository.Span{{IngestedAt: ingested}, {}}

	stampBoringExpiry(spans, 0, now)
	if spans[0].ExpiresAt != nil || spans[1].ExpiresAt != nil {
		t.Fatal("zero retention must leave expires_at nil")
	}
	stampBoringExpiry(spans, time.Hour, now)
	if !spans[0].ExpiresAt.Equal(ingested.Add(time.Hour)) {
		t.Errorf("ingested span expiry = %v", spans[0].ExpiresAt)
	}
	if !spans[1].ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("fallback span expiry = %v", spans[1].ExpiresAt)
	}
}

func TestSplitBoringTraces(t *testing.T) {
	spans := []repository.Span{
		{TraceID: "a", ProjectID: 1},
		{TraceID: "b", ProjectID: 2},
		{TraceID: "a", ProjectID: 1},
		{TraceID: "c", ProjectID: 3},
	}
	kept, order, boring := splitBoringTraces(spans, map[string]struct{}{"c": {}})
	if len(kept) != 1 || kept[0].TraceID != "c" {
		t.Fatalf("kept = %+v", kept)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("order = %v", order)
	}
	if len(boring["a"].spans) != 2 || boring["b"].projectID != 2 {
		t.Fatalf("boring = %+v", boring)
	}
}
