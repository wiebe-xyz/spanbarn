package observability

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// TracingConfig holds self-instrumentation settings.
type TracingConfig struct {
	Endpoint    string
	APIKey      string
	Service     string
	Environment string
}

// InitTracing sets up OpenTelemetry self-instrumentation (dogfooding).
// It configures an OTLP HTTP exporter pointed at SpanBarn's own /v1/traces endpoint.
// Returns a shutdown function that flushes pending spans.
func InitTracing(cfg TracingConfig) func() {
	if cfg.Endpoint == "" || cfg.APIKey == "" {
		return func() {}
	}

	ctx := context.Background()

	endpoint := cfg.Endpoint
	opts := []otlptracehttp.Option{
		otlptracehttp.WithHeaders(map[string]string{
			"X-SpanBarn-Api-Key": cfg.APIKey,
		}),
	}

	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		endpoint = u.Host
		if u.Scheme != "https" {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
	} else {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	opts = append(opts, otlptracehttp.WithEndpoint(endpoint))

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		slog.Error("failed to create OTLP exporter", "error", err)
		return func() {}
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.Service),
			semconv.DeploymentEnvironment(cfg.Environment),
		),
	)
	if err != nil {
		slog.Error("failed to create OTel resource", "error", err)
		return func() {}
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func() {
		_ = tp.Shutdown(context.Background())
	}
}

// TracingMiddleware returns OTel HTTP middleware for incoming requests.
// It filters out high-frequency health/metrics endpoints to avoid noise.
func TracingMiddleware(next http.Handler) http.Handler {
	otelHandler := otelhttp.NewHandler(next, "http.request")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/v1/health" || path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		otelHandler.ServeHTTP(w, r)
	})
}

// Setup initializes the full observability stack (BugBarn + self-tracing)
// and returns a composite logger and shutdown function.
func Setup(version string) (*slog.Logger, func()) {
	env := getenvDefault("SPANBARN_ENVIRONMENT", "development")

	jsonHandler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	var handler slog.Handler = jsonHandler
	var bugbarnClient *BugBarnClient

	bbEndpoint := os.Getenv("SPANBARN_BUGBARN_ENDPOINT")
	bbAPIKey := os.Getenv("SPANBARN_BUGBARN_API_KEY")
	if bbEndpoint != "" && bbAPIKey != "" {
		bugbarnClient = NewBugBarnClient(BugBarnConfig{
			Endpoint:    bbEndpoint,
			APIKey:      bbAPIKey,
			Project:     "spanbarn",
			Environment: env,
			Version:     version,
		})
		handler = NewBugBarnHandler(jsonHandler, bugbarnClient)
	}

	logger := slog.New(handler)

	selfAPIKey := os.Getenv("SPANBARN_SELF_API_KEY")
	if selfAPIKey == "" {
		selfAPIKey = os.Getenv("SPANBARN_API_KEY")
	}

	shutdownTracing := InitTracing(TracingConfig{
		Endpoint:    os.Getenv("SPANBARN_SELF_ENDPOINT"),
		APIKey:      selfAPIKey,
		Service:     "spanbarn",
		Environment: env,
	})

	shutdown := func() {
		shutdownTracing()
		if bugbarnClient != nil {
			bugbarnClient.Shutdown()
		}
	}

	return logger, shutdown
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
