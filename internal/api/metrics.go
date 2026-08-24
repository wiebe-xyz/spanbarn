package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/wiebe-xyz/spanbarn/internal/ingest"
)

// Metrics holds Prometheus metrics for SpanBarn.
type Metrics struct {
	SpansIngested  prometheus.Counter
	SpansProcessed prometheus.Counter
	OrphanedIngest *prometheus.CounterVec
	HTTPRequests   *prometheus.CounterVec
	HTTPDuration   *prometheus.HistogramVec
	registry       *prometheus.Registry
	metricsHandler http.Handler
}

// NewMetrics creates and registers Prometheus metrics.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		SpansIngested: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "spans_ingested_total",
			Help: "Total number of spans ingested.",
		}),
		SpansProcessed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "spans_processed_total",
			Help: "Total number of spans processed.",
		}),
		// Labelled by signal only (traces|metrics|logs). Deliberately not by
		// service.name: that is client-supplied and unbounded, and this counter
		// fires exactly when a client is misconfigured — the worst moment to
		// hand it control of label cardinality. The service name is in the
		// throttled warn log instead.
		OrphanedIngest: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "orphaned_ingest_total",
			Help: "Telemetry accepted on the global/admin key and stamped project 0, making it invisible in every per-project view. Non-zero means a client is authenticating with the wrong key (or, on a single-tenant install, that project 0 is simply in use).",
		}, []string{"signal"}),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests by route, method, and status.",
		}, []string{"route", "method", "status"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method"}),
		registry: reg,
	}

	reg.MustRegister(m.SpansIngested)
	reg.MustRegister(m.SpansProcessed)
	reg.MustRegister(m.OrphanedIngest)
	reg.MustRegister(m.HTTPRequests)
	reg.MustRegister(m.HTTPDuration)

	m.metricsHandler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return m
}

// RegisterTraceBuffer exposes the tail-sampling trace buffer's occupancy and
// loss. Cap pressure was previously a log line only — which is exactly how a
// month of missing CronJob traces went unnoticed — so it gets first-class
// metrics: occupancy to see the cap coming, and loss split by what it costs.
func (m *Metrics) RegisterTraceBuffer(stats func() ingest.BufferStats) {
	if stats == nil {
		return
	}
	gauge := func(name, help string, read func(ingest.BufferStats) float64) prometheus.Collector {
		return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help}, func() float64 {
			return read(stats())
		})
	}
	counter := func(name, help string, read func(ingest.BufferStats) float64) prometheus.Collector {
		return prometheus.NewCounterFunc(prometheus.CounterOpts{Name: name, Help: help}, func() float64 {
			return read(stats())
		})
	}
	m.registry.MustRegister(
		gauge("trace_buffer_spans", "Spans currently held in the tail-sampling trace buffer.",
			func(s ingest.BufferStats) float64 { return float64(s.BufferedSpans) }),
		gauge("trace_buffer_traces", "Traces currently held in the tail-sampling trace buffer.",
			func(s ingest.BufferStats) float64 { return float64(s.BufferedTraces) }),
		gauge("trace_buffer_max_spans", "Configured span cap for the trace buffer (SPANBARN_TRACE_BUFFER_MAX_SPANS); 0 means uncapped.",
			func(s ingest.BufferStats) float64 { return float64(s.MaxSpans) }),
		counter("trace_buffer_spans_shed_total", "Spans freed at cap from traces sampling was going to discard anyway. Expected under load; costs no stored telemetry.",
			func(s ingest.BufferStats) float64 { return float64(s.EvictedSacrificialSpans) }),
		counter("trace_buffer_spans_lost_total", "Spans lost at cap that would have been stored: evicted from keepable traces, refused outright, or undeliverable to the drain. Non-zero means the cap is too small for the span rate.",
			func(s ingest.BufferStats) float64 {
				return float64(s.EvictedKeptSpans + s.RefusedSpans + s.UndeliveredSpans)
			}),
	)
}

// Handler returns an http.Handler for the /metrics endpoint.
// If metricsToken is non-empty, Bearer token authentication is required.
func (m *Metrics) Handler(metricsToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if metricsToken != "" {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || !secretEqual(strings.TrimPrefix(auth, "Bearer "), metricsToken) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing bearer token")
				return
			}
		}
		m.metricsHandler.ServeHTTP(w, r)
	})
}

// MetricsMiddleware returns an HTTP middleware that records request metrics.
func MetricsMiddleware(m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			duration := time.Since(start).Seconds()
			route := normalizeRoute(r.URL.Path)
			m.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(sw.status)).Inc()
			m.HTTPDuration.WithLabelValues(route, r.Method).Observe(duration)
		})
	}
}

// normalizeRoute collapses path segments with IDs into a template form.
func normalizeRoute(path string) string {
	// Collapse /api/v1/traces/<id> and /api/v1/services/<id> patterns.
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if i > 3 && len(p) > 8 {
			// Likely a trace ID or similar — replace with placeholder.
			parts[i] = ":id"
		}
	}
	return strings.Join(parts, "/")
}
