package observability

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const selfInstrumentUA = "spanbarn-self-instrument"

type selfInstrumentKey struct{}

// IsSelfInstrument reports whether the request originates from SpanBarn's own
// OTLP exporter, so handlers can skip creating spans and avoid feedback loops.
func IsSelfInstrument(ctx context.Context) bool {
	v, _ := ctx.Value(selfInstrumentKey{}).(bool)
	return v
}

// TracingConfig holds self-instrumentation settings.
type TracingConfig struct {
	Endpoint    string
	APIKey      string
	Service     string
	Version     string
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
	var opts []otlptracehttp.Option

	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		endpoint = u.Host
		if u.Scheme != "https" {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
	} else {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	opts = append(opts,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithHeaders(map[string]string{
			"X-SpanBarn-Api-Key": cfg.APIKey,
			"User-Agent":         selfInstrumentUA,
		}),
	)

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		slog.Error("failed to create OTLP exporter", "error", err)
		return func() {}
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.Service),
		semconv.DeploymentEnvironment(cfg.Environment),
	}
	if cfg.Version != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.Version))
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(attrs...),
	)
	if err != nil {
		slog.Error("failed to create OTel resource", "error", err)
		return func() {}
	}

	// AlwaysSample so every span is recorded in memory; the samplingProcessor
	// drops non-error spans that fall outside the 1% trace-ID bucket at
	// export time. This lets error traces propagate fully while keeping
	// self-instrumentation volume near zero.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(
			newSamplingProcessor(
				sdktrace.NewBatchSpanProcessor(exporter),
				DefaultSelfSamplePercent,
			),
		),
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
// It filters out health/metrics endpoints and self-instrumentation exports
// to avoid noise and infinite feedback loops.
func TracingMiddleware(next http.Handler) http.Handler {
	otelHandler := otelhttp.NewHandler(next, "http.request")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/v1/health" || path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.Contains(r.UserAgent(), selfInstrumentUA) {
			ctx := context.WithValue(r.Context(), selfInstrumentKey{}, true)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		otelHandler.ServeHTTP(w, r)
	})
}

// SetupConfig holds values needed by the observability stack.
type SetupConfig struct {
	Version         string
	Environment     string
	BugBarnEndpoint string
	BugBarnAPIKey   string
	SelfEndpoint    string
	SelfAPIKey      string
}

// Setup initializes the full observability stack (BugBarn + self-tracing)
// and returns a composite logger and shutdown function.
func Setup(version string) (*slog.Logger, func()) {
	selfAPIKey := os.Getenv("SPANBARN_SELF_API_KEY")
	if selfAPIKey == "" {
		selfAPIKey = os.Getenv("SPANBARN_API_KEY")
	}
	return SetupWithConfig(SetupConfig{
		Version:         version,
		Environment:     getenvDefault("SPANBARN_ENVIRONMENT", "development"),
		BugBarnEndpoint: os.Getenv("SPANBARN_BUGBARN_ENDPOINT"),
		BugBarnAPIKey:   os.Getenv("SPANBARN_BUGBARN_API_KEY"),
		SelfEndpoint:    os.Getenv("SPANBARN_SELF_ENDPOINT"),
		SelfAPIKey:      selfAPIKey,
	})
}

// SetupWithConfig initializes observability from explicit config.
func SetupWithConfig(cfg SetupConfig) (*slog.Logger, func()) {
	jsonHandler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	var handler slog.Handler = jsonHandler
	var bugbarnClient *BugBarnClient
	var selfLogsH *SelfLogsHandler

	if cfg.BugBarnEndpoint != "" && cfg.BugBarnAPIKey != "" {
		bugbarnClient = NewBugBarnClient(BugBarnConfig{
			Endpoint:    cfg.BugBarnEndpoint,
			APIKey:      cfg.BugBarnAPIKey,
			Project:     "spanbarn",
			Environment: cfg.Environment,
			Version:     cfg.Version,
		})
		handler = NewBugBarnHandler(jsonHandler, bugbarnClient)
	}

	if cfg.SelfEndpoint != "" && cfg.SelfAPIKey != "" {
		selfLogsH = NewSelfLogsHandler(handler, cfg.SelfEndpoint, cfg.SelfAPIKey, "spanbarn", cfg.Version, cfg.Environment)
		handler = selfLogsH
	}

	logger := slog.New(handler)

	shutdownTracing := InitTracing(TracingConfig{
		Endpoint:    cfg.SelfEndpoint,
		APIKey:      cfg.SelfAPIKey,
		Service:     "spanbarn",
		Version:     cfg.Version,
		Environment: cfg.Environment,
	})

	shutdown := func() {
		shutdownTracing()
		if selfLogsH != nil {
			selfLogsH.Shutdown()
		}
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
