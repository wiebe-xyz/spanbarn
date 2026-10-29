package main

import (
	"log/slog"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
)

// newTraceBuffer builds the tail-sampling trace buffer from config. Every OTLP
// mode goes through here so the cap and TTL are tunable per deployment: the
// defaults imply a hard ceiling on sustained span rate, and a deployment that
// exceeds it starts shedding telemetry with no way to react short of a release.
func newTraceBuffer(cfg config.Config, lookup ingest.SampleRatioLookup, logger *slog.Logger) *ingest.TraceBuffer {
	ttl := time.Duration(cfg.TraceBufferTTLSeconds) * time.Second
	if cfg.TraceBufferMaxSpans <= 0 {
		logger.Warn("trace buffer span cap is disabled: buffered spans are live data the GC cannot reclaim, so ingest bursts can OOM this pod",
			"setting", "SPANBARN_TRACE_BUFFER_MAX_SPANS")
	}
	rules := ingest.RescueRules{
		SlowThresholdUs:  int64(cfg.SlowThresholdMS) * 1000,
		MinTracesPerHour: cfg.TraceBufferMinPerHour,
	}
	tb := ingest.NewTraceBufferWithRules(ttl, cfg.TraceBufferMaxSpans, rules, lookup, logger)
	tb.SetRootGrace(time.Duration(cfg.TraceBufferRootGraceSeconds) * time.Second)
	return tb
}

// registerTraceBufferGauges publishes trace-buffer occupancy and loss as
// self-metrics. Cap pressure used to be a log line only, which is how five
// daily CronJobs stayed unobservable for a month — make it a number on the
// Metrics page instead.
func registerTraceBufferGauges(rec *selfmetrics.Recorder, tb *ingest.TraceBuffer) {
	if rec == nil || tb == nil {
		return
	}
	rec.RegisterGauge("spanbarn.trace_buffer.spans", nil, func() float64 {
		return float64(tb.Stats().BufferedSpans)
	})
	rec.RegisterGauge("spanbarn.trace_buffer.traces", nil, func() float64 {
		return float64(tb.Stats().BufferedTraces)
	})
	// Fill is buffered spans over the cap, 0 without a cap. Near 1 for long
	// means the cap sheds unsampled traces all the time.
	rec.RegisterGauge("spanbarn.trace_buffer.fill", nil, func() float64 {
		st := tb.Stats()
		if st.MaxSpans <= 0 {
			return 0
		}
		return float64(st.BufferedSpans) / float64(st.MaxSpans)
	})
	// Spans the cap shed or decided early since start: unsampled traces it
	// evicted and traces it rescued before they came due.
	rec.RegisterGauge("spanbarn.trace_buffer.shed_spans", nil, func() float64 {
		st := tb.Stats()
		return float64(st.EvictedSacrificialSpans + st.RescuedAtCapSpans)
	})
	// Only counts loss that costs stored telemetry; shedding traces sampling
	// was going to discard is the cap working, not an incident.
	rec.RegisterGauge("spanbarn.trace_buffer.spans_lost", nil, func() float64 {
		st := tb.Stats()
		return float64(st.RefusedSpans + st.EvictedKeptSpans + st.UndeliveredSpans)
	})
	// Decisions late spans follow. An eviction means the cap dropped a
	// decision before the TTL, and a late span of that trace was decided again
	// on its own.
	rec.RegisterGauge("spanbarn.trace_buffer.decided", nil, func() float64 {
		return float64(tb.Stats().DecidedTraces)
	})
	rec.RegisterGauge("spanbarn.trace_buffer.decided_evicted", nil, func() float64 {
		return float64(tb.Stats().DecidedEvicted)
	})
	rec.RegisterGauge("spanbarn.trace_buffer.late_spans", nil, func() float64 {
		return float64(tb.Stats().LateSpansKept)
	})
}
