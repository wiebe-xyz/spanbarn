package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Row builders for the contention writers, one batch per tick.

func contBatch[T any](n int, g *generator, now time.Time, mk func(*generator, time.Time) T) []T {
	out := make([]T, n)
	for i := range out {
		out[i] = mk(g, now)
	}
	return out
}

// contSpans builds one trace of n spans: a root and n-1 children.
func contSpans(g *generator, now time.Time, n int) []repository.Span {
	project, traceID, rootID := g.project(), g.hexID(32), g.hexID(16)
	svc := services[g.rng.IntN(len(services))]
	out := make([]repository.Span, n)
	for i := range out {
		s := repository.Span{
			ProjectID: project, TraceID: traceID, SpanID: g.hexID(16), ParentSpanID: rootID,
			Name: operations[3+g.rng.IntN(len(operations)-3)], Service: svc, Kind: "server", Status: "ok",
			StartTimeUs: now.UnixMicro(), DurationUs: int64(1_000 + g.rng.IntN(80_000)),
			Attributes: fmt.Sprintf(`{"http.response.status_code":200,"user.id":"u%d"}`, g.rng.IntN(5_000)),
			Events:     "[]",
		}
		if i == 0 {
			s.SpanID, s.ParentSpanID, s.Name = rootID, "", operations[g.rng.IntN(3)]
		}
		out[i] = s
	}
	return out
}

func contMetric(g *generator, now time.Time) model.MetricRecord {
	return model.MetricRecord{
		ProjectID: g.project(), Name: metricKeys[g.rng.IntN(len(metricKeys))], Unit: "ms",
		Type: model.MetricTypeGauge, TimeUnixNano: uint64(now.UnixNano()), Value: g.rng.Float64() * 100, Count: 1,
		Attributes: json.RawMessage(fmt.Sprintf(`{"service.name":%q,"host.name":"node-%d"}`,
			services[g.rng.IntN(len(services))], g.rng.IntN(12))),
	}
}

func contLog(g *generator, now time.Time) model.LogRecord {
	return model.LogRecord{
		ProjectID: g.project(), TraceID: g.hexID(32), SpanID: g.hexID(16), SeverityNumber: 9, SeverityText: "INFO",
		TimeUnixNano: uint64(now.UnixNano()), Body: g.text(contLogBodyBytes),
		Attributes: json.RawMessage(fmt.Sprintf(`{"service.name":%q}`, services[g.rng.IntN(len(services))])),
	}
}

func contPrompt(g *generator, now time.Time) repository.PromptRecord {
	in, out := int64(200+g.rng.IntN(2_000)), int64(50+g.rng.IntN(800))
	return repository.PromptRecord{
		ProjectID: g.project(), TraceID: g.hexID(32), SpanID: g.hexID(16), Service: "llm", Name: "chat",
		GenAISystem: "openai", Model: models[g.rng.IntN(len(models))],
		PromptBody: g.text(contPromptBytes), ResponseBody: g.text(contPromptBytes),
		InputTokens: in, OutputTokens: out, TotalTokens: in + out, CostUSD: float64(in+out) * 1e-6,
		DurationUs: int64(200_000 + g.rng.IntN(4_000_000)), StartTimeUs: now.UnixMicro(), Status: "ok",
	}
}

func contErrorSample(g *generator, now time.Time) repository.Span {
	return repository.Span{
		ProjectID: g.project(), TraceID: g.hexID(32), SpanID: g.hexID(16),
		Name: operations[g.rng.IntN(len(operations))], Service: services[g.rng.IntN(len(services))],
		Kind: "server", Status: "error", StartTimeUs: now.UnixMicro(), DurationUs: int64(g.rng.IntN(500_000)),
		Attributes: `{"http.response.status_code":500}`,
		Events:     fmt.Sprintf(`[{"name":"exception","attributes":{"exception.message":%q}}]`, g.text(600)),
	}
}
