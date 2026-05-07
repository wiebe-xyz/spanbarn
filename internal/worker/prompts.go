package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func extractPromptRecords(spans []repository.Span) []repository.PromptRecord {
	var records []repository.PromptRecord
	for _, sp := range spans {
		attrs := parseAttrs(sp.Attributes)
		if attrs == nil {
			continue
		}
		system := strAttr(attrs, "gen_ai.system")
		if system == "" {
			continue
		}

		rec := repository.PromptRecord{
			ProjectID:    sp.ProjectID,
			TraceID:      sp.TraceID,
			SpanID:       sp.SpanID,
			ParentSpanID: sp.ParentSpanID,
			Service:      sp.Service,
			Name:         sp.Name,
			GenAISystem:  system,
			Model:        strAttr(attrs, "gen_ai.response.model"),
			DurationUs:   sp.DurationUs,
			Status:       sp.Status,
			FinishReason: strAttr(attrs, "gen_ai.response.finish_reasons"),
			StartTimeUs:  sp.StartTimeUs,
		}

		if rec.Model == "" {
			rec.Model = strAttr(attrs, "gen_ai.request.model")
		}

		if v, ok := floatAttr(attrs, "gen_ai.request.temperature"); ok {
			rec.Temperature = &v
		}
		if v, ok := intAttr(attrs, "gen_ai.request.max_tokens"); ok {
			rec.MaxTokens = &v
		}

		rec.InputTokens = intAttrDefault(attrs, "gen_ai.usage.input_tokens", 0)
		rec.OutputTokens = intAttrDefault(attrs, "gen_ai.usage.output_tokens", 0)
		rec.TotalTokens = intAttrDefault(attrs, "gen_ai.usage.total_tokens", 0)
		if rec.TotalTokens == 0 {
			rec.TotalTokens = rec.InputTokens + rec.OutputTokens
		}

		rec.CostUSD = floatAttrDefault(attrs, "gen_ai.usage.cost", 0)

		rec.PromptTemplate = strAttr(attrs, "gen_ai.prompt.template")
		rec.Outcome = strAttr(attrs, "gen_ai.outcome")

		if v, ok := floatAttr(attrs, "gen_ai.quality_score"); ok {
			rec.QualityScore = &v
		}

		rec.FeatureFlagKey = strAttr(attrs, "feature_flag.key")
		rec.FeatureFlagVariant = strAttr(attrs, "feature_flag.variant")

		rec.PromptBody, rec.ResponseBody = extractBodies(sp.Events)

		if rec.PromptTemplate != "" {
			rec.PromptHash = hashString(rec.PromptTemplate)
		} else if rec.PromptBody != "" {
			rec.PromptHash = hashString(rec.Name)
		}

		records = append(records, rec)
	}
	return records
}

func parseAttrs(raw string) map[string]any {
	if raw == "" || raw == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

func strAttr(attrs map[string]any, key string) string {
	v, ok := attrs[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func floatAttr(attrs map[string]any, key string) (float64, bool) {
	v, ok := attrs[key]
	if !ok {
		return 0, false
	}
	switch f := v.(type) {
	case float64:
		return f, true
	case int64:
		return float64(f), true
	case json.Number:
		n, err := f.Float64()
		return n, err == nil
	}
	return 0, false
}

func intAttr(attrs map[string]any, key string) (int64, bool) {
	v, ok := attrs[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

func intAttrDefault(attrs map[string]any, key string, def int64) int64 {
	if v, ok := intAttr(attrs, key); ok {
		return v
	}
	return def
}

func floatAttrDefault(attrs map[string]any, key string, def float64) float64 {
	if v, ok := floatAttr(attrs, key); ok {
		return v
	}
	return def
}

func extractBodies(eventsJSON string) (prompt, response string) {
	if eventsJSON == "" || eventsJSON == "[]" {
		return "", ""
	}
	var events []map[string]any
	if err := json.Unmarshal([]byte(eventsJSON), &events); err != nil {
		return "", ""
	}

	var promptParts, responseParts []string
	for _, ev := range events {
		name, _ := ev["name"].(string)
		body, _ := ev["body"].(string)
		if body == "" {
			if attrs, ok := ev["attributes"].(map[string]any); ok {
				if content, ok := attrs["gen_ai.content"].(string); ok {
					body = content
				}
			}
		}
		switch {
		case strings.HasPrefix(name, "gen_ai.content.prompt"), name == "gen_ai.prompt":
			promptParts = append(promptParts, body)
		case strings.HasPrefix(name, "gen_ai.content.completion"), name == "gen_ai.completion":
			responseParts = append(responseParts, body)
		}
	}
	return strings.Join(promptParts, "\n"), strings.Join(responseParts, "\n")
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}
