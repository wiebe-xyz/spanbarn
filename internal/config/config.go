package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// RetentionConfig groups the data-retention windows.
type RetentionConfig struct {
	FullHours          int // SPANBARN_RETENTION_FULL_HOURS
	AggregatedDays     int // SPANBARN_RETENTION_AGGREGATED_DAYS
	ErrorDays          int // SPANBARN_RETENTION_ERROR_DAYS
	InterestingHours   int // SPANBARN_RETENTION_INTERESTING_HOURS
	BoringMinutes      int // SPANBARN_BORING_RETENTION_MINUTES — short retention for sampled boring spans
	MetricsDays        int // SPANBARN_METRICS_RETENTION_DAYS — how long to keep raw metric data points
	LogHours           int // SPANBARN_LOG_RETENTION_HOURS — how long to keep log records (default 24)
	ErrorLogDays       int // SPANBARN_ERROR_LOG_RETENTION_DAYS — how long to keep logs for error traces (default 30)
	DeleteBatchYieldMS int // SPANBARN_RETENTION_DELETE_BATCH_YIELD_MS — pause between batched retention deletes so the WAL checkpoint and reads aren't starved (default 200)
}

// OIDCConfig groups the IamBarn OIDC integration settings. When Issuer,
// ClientID, ClientSecret and RedirectURL are all set, OIDC login is offered
// alongside local auth.
type OIDCConfig struct {
	Issuer            string   // SPANBARN_OIDC_ISSUER
	ClientID          string   // SPANBARN_OIDC_CLIENT_ID
	ClientSecret      string   // SPANBARN_OIDC_CLIENT_SECRET
	RedirectURL       string   // SPANBARN_OIDC_REDIRECT_URL
	RequiredGroup     string   // SPANBARN_OIDC_REQUIRED_GROUP — defaults to "spanbarn-users"
	ResourceAudiences []string // SPANBARN_OIDC_RESOURCE_AUDIENCES — CSV of audiences accepted on IamBarn access tokens (sb CLI)
	CLIClientID       string   // SPANBARN_OIDC_CLI_CLIENT_ID — public IamBarn client the sb device-code flow uses
}

// SelfConfig groups self-instrumentation (SpanBarn reporting to itself/BugBarn).
type SelfConfig struct {
	Endpoint           string // SPANBARN_SELF_ENDPOINT
	APIKey             string // SPANBARN_SELF_API_KEY
	MetricsIntervalSec int    // SPANBARN_SELF_METRICS_INTERVAL_SECONDS — self-metrics export cadence (default 30)
	MetricsDisabled    bool   // SPANBARN_SELF_METRICS_DISABLED — disable self-metrics export
}

// BugBarnConfig groups error self-reporting to BugBarn.
type BugBarnConfig struct {
	Endpoint string // SPANBARN_BUGBARN_ENDPOINT
	APIKey   string // SPANBARN_BUGBARN_API_KEY
}

// FunnelBarnConfig groups analytics self-reporting to FunnelBarn.
type FunnelBarnConfig struct {
	Endpoint string // SPANBARN_FUNNELBARN_ENDPOINT
	APIKey   string // SPANBARN_FUNNELBARN_API_KEY
	Project  string // SPANBARN_FUNNELBARN_PROJECT
}

// Config holds all SPANBARN_* environment variables with sensible defaults.
type Config struct {
	Addr                     string
	PublicURL                string
	DBPath                   string
	SpoolDir                 string
	APIKey                   string
	APIKeySHA256             string
	AdminUsername            string
	AdminPassword            string
	AdminPasswordBcrypt      string
	SessionSecret            string
	SessionTTLSeconds        int
	MaxBodyBytes             int64
	MaxSpoolBytes            int64
	Retention                RetentionConfig
	WALTruncateThresholdMB   int  // SPANBARN_WAL_TRUNCATE_THRESHOLD_MB — under Litestream the writer checkpoints PASSIVE (no forced re-snapshot) but escalates to one TRUNCATE when the WAL grows past this size, to bound it under sustained read load (default 256; 0 disables escalation)
	SpanStagingEnabled       bool // SPANBARN_SPAN_STAGING_ENABLED — when true, the redis worker appends consumed spans to spans_staging (cheap) and a background flusher does accumulation+classification+indexed storage per complete trace off the hot path (default false)
	TraceBufferWindowSeconds int  // SPANBARN_TRACE_BUFFER_WINDOW_SECONDS — how long a trace's spans buffer in spans_staging before the flusher treats the trace as complete (default 90)
	StagingMaxAgeSeconds     int  // SPANBARN_STAGING_MAX_AGE_SECONDS — hard backstop: spans_staging rows older than this are dropped unconditionally so the table can never grow without bound (default 900)
	IngestSampleRate         float64
	SlowThresholdMS          int
	AggregationInterval      string
	AllowedOrigins           []string
	Self                     SelfConfig
	BugBarn                  BugBarnConfig
	FunnelBarn               FunnelBarnConfig
	Environment              string
	LoginRatePerMinute       int
	IngestRatePerMinute      int
	APIRatePerMinute         int
	MetricsToken             string
	QueryTimeoutSeconds      int
	RedisURL                 string
	RedisQueueURL            string // SPANBARN_REDIS_QUEUE_URL — write queue Redis (separate from cache)
	CacheTTLSeconds          int
	Mode                     string // "standalone" (default), "reader", "writer", or "ingest" (legacy)
	WriterURL                string // URL of writer pod, used when Mode=ingest (legacy)
	OIDC                     OIDCConfig
	GRPCAddr                 string // SPANBARN_GRPC_ADDR — gRPC listener for OTLP; empty = disabled
	TrustProxy               bool   // SPANBARN_TRUST_PROXY — trust X-Forwarded-For/X-Real-IP for client IP (rate limiting); defaults to true outside dev
}

// Load reads configuration from SPANBARN_* environment variables with defaults.
func Load() Config {
	cfg := Config{
		Addr:                getenv("SPANBARN_ADDR", ":8080"),
		PublicURL:           os.Getenv("SPANBARN_PUBLIC_URL"),
		DBPath:              getenv("SPANBARN_DB_PATH", ".data/spanbarn.db"),
		SpoolDir:            getenv("SPANBARN_SPOOL_DIR", ".data/spool"),
		APIKey:              os.Getenv("SPANBARN_API_KEY"),
		APIKeySHA256:        os.Getenv("SPANBARN_API_KEY_SHA256"),
		AdminUsername:       os.Getenv("SPANBARN_ADMIN_USERNAME"),
		AdminPassword:       os.Getenv("SPANBARN_ADMIN_PASSWORD"),
		AdminPasswordBcrypt: os.Getenv("SPANBARN_ADMIN_PASSWORD_BCRYPT"),
		SessionSecret:       os.Getenv("SPANBARN_SESSION_SECRET"),
		SessionTTLSeconds:   getenvInt("SPANBARN_SESSION_TTL_SECONDS", 43200),
		MaxBodyBytes:        getenvInt64("SPANBARN_MAX_BODY_BYTES", 1<<20),
		MaxSpoolBytes:       getenvInt64("SPANBARN_MAX_SPOOL_BYTES", 0),
		Retention: RetentionConfig{
			FullHours:          getenvInt("SPANBARN_RETENTION_FULL_HOURS", 72),
			AggregatedDays:     getenvInt("SPANBARN_RETENTION_AGGREGATED_DAYS", 30),
			ErrorDays:          getenvInt("SPANBARN_RETENTION_ERROR_DAYS", 90),
			InterestingHours:   getenvInt("SPANBARN_RETENTION_INTERESTING_HOURS", 168),
			BoringMinutes:      getenvInt("SPANBARN_BORING_RETENTION_MINUTES", 30),
			MetricsDays:        getenvInt("SPANBARN_METRICS_RETENTION_DAYS", 90),
			LogHours:           getenvInt("SPANBARN_LOG_RETENTION_HOURS", 24),
			ErrorLogDays:       getenvInt("SPANBARN_ERROR_LOG_RETENTION_DAYS", 30),
			DeleteBatchYieldMS: getenvInt("SPANBARN_RETENTION_DELETE_BATCH_YIELD_MS", 200),
		},
		WALTruncateThresholdMB:   getenvInt("SPANBARN_WAL_TRUNCATE_THRESHOLD_MB", 256),
		SpanStagingEnabled:       getenvInt("SPANBARN_SPAN_STAGING_ENABLED", 0) != 0,
		TraceBufferWindowSeconds: getenvInt("SPANBARN_TRACE_BUFFER_WINDOW_SECONDS", 90),
		StagingMaxAgeSeconds:     getenvInt("SPANBARN_STAGING_MAX_AGE_SECONDS", 900),
		IngestSampleRate:         getenvFloat("SPANBARN_INGEST_SAMPLE_RATE", 1.0),
		SlowThresholdMS:          getenvInt("SPANBARN_SLOW_THRESHOLD_MS", 500),
		AggregationInterval:      getenv("SPANBARN_AGGREGATION_INTERVAL", "1m"),
		Self: SelfConfig{
			Endpoint:           os.Getenv("SPANBARN_SELF_ENDPOINT"),
			APIKey:             os.Getenv("SPANBARN_SELF_API_KEY"),
			MetricsIntervalSec: getenvInt("SPANBARN_SELF_METRICS_INTERVAL_SECONDS", 30),
			MetricsDisabled:    getenvInt("SPANBARN_SELF_METRICS_DISABLED", 0) != 0,
		},
		BugBarn: BugBarnConfig{
			Endpoint: os.Getenv("SPANBARN_BUGBARN_ENDPOINT"),
			APIKey:   os.Getenv("SPANBARN_BUGBARN_API_KEY"),
		},
		FunnelBarn: FunnelBarnConfig{
			Endpoint: os.Getenv("SPANBARN_FUNNELBARN_ENDPOINT"),
			APIKey:   os.Getenv("SPANBARN_FUNNELBARN_API_KEY"),
			Project:  getenv("SPANBARN_FUNNELBARN_PROJECT", "spanbarn"),
		},
		Environment:         getenv("SPANBARN_ENVIRONMENT", "development"),
		LoginRatePerMinute:  getenvInt("SPANBARN_LOGIN_RATE_PER_MINUTE", 10),
		IngestRatePerMinute: getenvInt("SPANBARN_INGEST_RATE_PER_MINUTE", 1000),
		APIRatePerMinute:    getenvInt("SPANBARN_API_RATE_PER_MINUTE", 300),
		MetricsToken:        os.Getenv("SPANBARN_METRICS_TOKEN"),
		QueryTimeoutSeconds: getenvInt("SPANBARN_QUERY_TIMEOUT_SECONDS", 30),
		RedisURL:            os.Getenv("SPANBARN_REDIS_URL"),
		RedisQueueURL:       os.Getenv("SPANBARN_REDIS_QUEUE_URL"),
		CacheTTLSeconds:     getenvInt("SPANBARN_CACHE_TTL_SECONDS", 30),
		Mode:                getenv("SPANBARN_MODE", "standalone"),
		WriterURL:           os.Getenv("SPANBARN_WRITER_URL"),
		OIDC: OIDCConfig{
			Issuer:        os.Getenv("SPANBARN_OIDC_ISSUER"),
			ClientID:      os.Getenv("SPANBARN_OIDC_CLIENT_ID"),
			ClientSecret:  os.Getenv("SPANBARN_OIDC_CLIENT_SECRET"),
			RedirectURL:   os.Getenv("SPANBARN_OIDC_REDIRECT_URL"),
			RequiredGroup: getenv("SPANBARN_OIDC_REQUIRED_GROUP", "spanbarn-users"),
			CLIClientID:   os.Getenv("SPANBARN_OIDC_CLI_CLIENT_ID"),
		},
		GRPCAddr: getenv("SPANBARN_GRPC_ADDR", ":4317"),
	}

	if raw := os.Getenv("SPANBARN_ALLOWED_ORIGINS"); raw != "" {
		for _, o := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(o); trimmed != "" {
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, trimmed)
			}
		}
	}

	if raw := os.Getenv("SPANBARN_OIDC_RESOURCE_AUDIENCES"); raw != "" {
		for _, a := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(a); trimmed != "" {
				cfg.OIDC.ResourceAudiences = append(cfg.OIDC.ResourceAudiences, trimmed)
			}
		}
	}

	// Trust proxy headers for client-IP determination by default in every named
	// deployment (which always sits behind Caddy/Nginx), but not in dev where the
	// app is reached directly and headers would be client-spoofable. Explicit
	// SPANBARN_TRUST_PROXY overrides either way.
	cfg.TrustProxy = getenvBool("SPANBARN_TRUST_PROXY", !cfg.IsDevEnvironment())

	return cfg
}

// IsDevEnvironment reports whether the configured environment is a local/dev
// one where relaxed security defaults (e.g. an empty session secret) are
// tolerated. Every named deployment environment (testing, staging, production)
// is treated as non-dev.
func (c Config) IsDevEnvironment() bool {
	switch strings.ToLower(strings.TrimSpace(c.Environment)) {
	case "", "development", "dev", "local":
		return true
	default:
		return false
	}
}

// Validate checks configuration invariants required to run an API server safely.
// It is called for the long-running server modes, not the CLI subcommands.
func (c Config) Validate() error {
	if c.SessionSecret == "" && !c.IsDevEnvironment() {
		return fmt.Errorf("SPANBARN_SESSION_SECRET must be set in the %q environment: "+
			"it signs session tokens and derives per-project setup keys, so a missing "+
			"secret makes both forgeable", c.Environment)
	}
	return nil
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if raw := os.Getenv(key); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func getenvInt64(key string, fallback int64) int64 {
	if raw := os.Getenv(key); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func getenvBool(key string, fallback bool) bool {
	if raw := os.Getenv(key); raw != "" {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

func getenvFloat(key string, fallback float64) float64 {
	if raw := os.Getenv(key); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 0 && parsed <= 1 {
			return parsed
		}
	}
	return fallback
}
