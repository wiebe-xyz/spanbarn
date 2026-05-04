package observability

import (
	"log/slog"
	"net/http"
	"os"

	spanbarn "github.com/wiebe-xyz/spanbarn-go"
)

// TracingConfig holds self-instrumentation settings.
type TracingConfig struct {
	Endpoint    string
	APIKey      string
	Service     string
	Environment string
}

// InitTracing sets up SpanBarn self-instrumentation (dogfooding).
// Returns a shutdown function that flushes pending spans.
func InitTracing(cfg TracingConfig) func() {
	if cfg.Endpoint == "" || cfg.APIKey == "" {
		return func() {}
	}

	spanbarn.Init(spanbarn.Config{
		Endpoint:    cfg.Endpoint,
		APIKey:      cfg.APIKey,
		Service:     cfg.Service,
		Environment: cfg.Environment,
	})

	return func() {
		_ = spanbarn.Shutdown()
	}
}

// TracingMiddleware returns the SpanBarn HTTP middleware for incoming requests.
// It filters out high-frequency health/metrics endpoints to avoid noise.
func TracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/v1/health" || path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		spanbarn.HTTPMiddleware(next).ServeHTTP(w, r)
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

	shutdownTracing := InitTracing(TracingConfig{
		Endpoint:    os.Getenv("SPANBARN_SELF_ENDPOINT"),
		APIKey:      os.Getenv("SPANBARN_SELF_API_KEY"),
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
