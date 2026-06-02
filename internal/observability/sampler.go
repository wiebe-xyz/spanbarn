package observability

import (
	"context"
	"encoding/binary"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/codes"
)

// DefaultSelfSamplePercent is the percentage of non-error traces that
// Spanbarn exports when self-instrumenting. 1 means 1 in every 100 traces.
const DefaultSelfSamplePercent = 1

// ShouldSampleTrace reports whether a trace ID should be sampled given a
// percentage (1–100). The decision is deterministic: the same trace ID always
// produces the same result, so a browser frontend using the same traceparent
// can apply identical filtering before sending spans.
//
// Algorithm: interpret the first 8 bytes of the trace ID as a big-endian
// uint64, then check if value % 100 < percent.
//
// JavaScript equivalent:
//
//	const upper = BigInt('0x' + traceId.slice(0, 16))
//	const sampled = upper % 100n < BigInt(percent)
func ShouldSampleTrace(id trace.TraceID, percent int) bool {
	upper := binary.BigEndian.Uint64(id[:8])
	return int(upper%100) < percent
}

// samplingProcessor wraps a SpanProcessor and applies tail-based sampling
// at OnEnd. Error spans are always forwarded. Non-error spans are forwarded
// only when their trace ID falls within the sample percentage.
type samplingProcessor struct {
	delegate sdktrace.SpanProcessor
	percent  int
}

func newSamplingProcessor(delegate sdktrace.SpanProcessor, percent int) *samplingProcessor {
	return &samplingProcessor{delegate: delegate, percent: percent}
}

func (p *samplingProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	p.delegate.OnStart(parent, s)
}

func (p *samplingProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if s.Status().Code == codes.Error {
		p.delegate.OnEnd(s)
		return
	}
	if ShouldSampleTrace(s.SpanContext().TraceID(), p.percent) {
		p.delegate.OnEnd(s)
	}
}

func (p *samplingProcessor) Shutdown(ctx context.Context) error {
	return p.delegate.Shutdown(ctx)
}

func (p *samplingProcessor) ForceFlush(ctx context.Context) error {
	return p.delegate.ForceFlush(ctx)
}
