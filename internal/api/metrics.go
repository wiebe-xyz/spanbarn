package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds Prometheus metrics for SpanBarn.
type Metrics struct {
	SpansIngested  prometheus.Counter
	SpansProcessed prometheus.Counter
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
	reg.MustRegister(m.HTTPRequests)
	reg.MustRegister(m.HTTPDuration)

	m.metricsHandler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return m
}

// Handler returns an http.Handler for the /metrics endpoint.
// If metricsToken is non-empty, Bearer token authentication is required.
func (m *Metrics) Handler(metricsToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if metricsToken != "" {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != metricsToken {
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
