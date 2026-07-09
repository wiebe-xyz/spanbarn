package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds all SPANBARN_* environment variables with sensible defaults.
type Config struct {
	Addr                        string
	PublicURL                   string
	DBPath                      string
	SpoolDir                    string
	APIKey                      string
	APIKeySHA256                string
	AdminUsername               string
	AdminPassword               string
	AdminPasswordBcrypt         string
	SessionSecret               string
	SessionTTLSeconds           int
	MaxBodyBytes                int64
	MaxSpoolBytes               int64
	RetentionFullHours          int
	RetentionAggregatedDays     int
	RetentionErrorDays          int
	RetentionInterestingHours   int
	BoringRetentionMinutes      int // SPANBARN_BORING_RETENTION_MINUTES — short retention for sampled boring spans
	MetricsRetentionDays        int // SPANBARN_METRICS_RETENTION_DAYS — how long to keep raw metric data points
	LogRetentionHours           int // SPANBARN_LOG_RETENTION_HOURS — how long to keep log records (default 24)
	ErrorLogRetentionDays       int // SPANBARN_ERROR_LOG_RETENTION_DAYS — how long to keep logs for error traces (default 30)
	RetentionDeleteBatchYieldMS int // SPANBARN_RETENTION_DELETE_BATCH_YIELD_MS — pause between batched retention deletes so the WAL checkpoint and reads aren't starved (default 200)
	WALTruncateThresholdMB      int // SPANBARN_WAL_TRUNCATE_THRESHOLD_MB — under Litestream the writer checkpoints PASSIVE (no forced re-snapshot) but escalates to one TRUNCATE when the WAL grows past this size, to bound it under sustained read load (default 256; 0 disables escalation)
	CheckpointSkipQueueDepth    int // SPANBARN_CHECKPOINT_SKIP_QUEUE_DEPTH — when the span write-queue holds more than this many pending batches, the writer skips WAL checkpoints (which can block the single connection up to busy_timeout) so it can pour throughput into draining the backlog; it still forces an occasional checkpoint to bound the WAL (default 100; 0 disables gating)
	IngestSampleRate            float64
	SlowThresholdMS             int
	AggregationInterval         string
	AllowedOrigins              []string
	SelfEndpoint                string
	SelfAPIKey                  string
	SelfMetricsIntervalSec      int  // SPANBARN_SELF_METRICS_INTERVAL_SECONDS — self-metrics export cadence (default 30)
	SelfMetricsDisabled         bool // SPANBARN_SELF_METRICS_DISABLED — disable self-metrics export
	BugBarnEndpoint             string
	BugBarnAPIKey               string
	FunnelBarnEndpoint          string
	FunnelBarnAPIKey            string
	FunnelBarnProject           string
	Environment                 string
	LoginRatePerMinute          int
	IngestRatePerMinute         int
	APIRatePerMinute            int
	MetricsToken                string
	QueryTimeoutSeconds         int
	RedisURL                    string
	RedisQueueURL               string // SPANBARN_REDIS_QUEUE_URL — write queue Redis (separate from cache)
	CacheTTLSeconds             int
	Mode                        string   // "standalone" (default), "reader", "writer", or "ingest" (legacy)
	WriterURL                   string   // URL of writer pod, used when Mode=ingest (legacy)
	OIDCIssuer                  string   // SPANBARN_OIDC_ISSUER — when all four OIDC vars are set, OIDC login is offered alongside local auth
	OIDCClientID                string   // SPANBARN_OIDC_CLIENT_ID
	OIDCClientSecret            string   // SPANBARN_OIDC_CLIENT_SECRET
	OIDCRedirectURL             string   // SPANBARN_OIDC_REDIRECT_URL
	OIDCRequiredGroup           string   // SPANBARN_OIDC_REQUIRED_GROUP — defaults to "spanbarn-users"
	OIDCResourceAudiences       []string // SPANBARN_OIDC_RESOURCE_AUDIENCES — CSV of audiences accepted on IamBarn access tokens (sb CLI)
	OIDCCLIClientID             string   // SPANBARN_OIDC_CLI_CLIENT_ID — public IamBarn client the sb device-code flow uses
	GRPCAddr                    string   // SPANBARN_GRPC_ADDR — gRPC listener for OTLP; empty = disabled
}

// Load reads configuration from SPANBARN_* environment variables with defaults.
func Load() Config {
	cfg := Config{
		Addr:                        getenv("SPANBARN_ADDR", ":8080"),
		PublicURL:                   os.Getenv("SPANBARN_PUBLIC_URL"),
		DBPath:                      getenv("SPANBARN_DB_PATH", ".data/spanbarn.db"),
		SpoolDir:                    getenv("SPANBARN_SPOOL_DIR", ".data/spool"),
		APIKey:                      os.Getenv("SPANBARN_API_KEY"),
		APIKeySHA256:                os.Getenv("SPANBARN_API_KEY_SHA256"),
		AdminUsername:               os.Getenv("SPANBARN_ADMIN_USERNAME"),
		AdminPassword:               os.Getenv("SPANBARN_ADMIN_PASSWORD"),
		AdminPasswordBcrypt:         os.Getenv("SPANBARN_ADMIN_PASSWORD_BCRYPT"),
		SessionSecret:               os.Getenv("SPANBARN_SESSION_SECRET"),
		SessionTTLSeconds:           getenvInt("SPANBARN_SESSION_TTL_SECONDS", 43200),
		MaxBodyBytes:                getenvInt64("SPANBARN_MAX_BODY_BYTES", 1<<20),
		MaxSpoolBytes:               getenvInt64("SPANBARN_MAX_SPOOL_BYTES", 0),
		RetentionFullHours:          getenvInt("SPANBARN_RETENTION_FULL_HOURS", 72),
		RetentionAggregatedDays:     getenvInt("SPANBARN_RETENTION_AGGREGATED_DAYS", 30),
		RetentionErrorDays:          getenvInt("SPANBARN_RETENTION_ERROR_DAYS", 90),
		RetentionInterestingHours:   getenvInt("SPANBARN_RETENTION_INTERESTING_HOURS", 168),
		BoringRetentionMinutes:      getenvInt("SPANBARN_BORING_RETENTION_MINUTES", 30),
		MetricsRetentionDays:        getenvInt("SPANBARN_METRICS_RETENTION_DAYS", 90),
		LogRetentionHours:           getenvInt("SPANBARN_LOG_RETENTION_HOURS", 24),
		ErrorLogRetentionDays:       getenvInt("SPANBARN_ERROR_LOG_RETENTION_DAYS", 30),
		RetentionDeleteBatchYieldMS: getenvInt("SPANBARN_RETENTION_DELETE_BATCH_YIELD_MS", 200),
		WALTruncateThresholdMB:      getenvInt("SPANBARN_WAL_TRUNCATE_THRESHOLD_MB", 256),
		CheckpointSkipQueueDepth:    getenvInt("SPANBARN_CHECKPOINT_SKIP_QUEUE_DEPTH", 100),
		IngestSampleRate:            getenvFloat("SPANBARN_INGEST_SAMPLE_RATE", 1.0),
		SlowThresholdMS:             getenvInt("SPANBARN_SLOW_THRESHOLD_MS", 500),
		AggregationInterval:         getenv("SPANBARN_AGGREGATION_INTERVAL", "1m"),
		SelfEndpoint:                os.Getenv("SPANBARN_SELF_ENDPOINT"),
		SelfAPIKey:                  os.Getenv("SPANBARN_SELF_API_KEY"),
		SelfMetricsIntervalSec:      getenvInt("SPANBARN_SELF_METRICS_INTERVAL_SECONDS", 30),
		SelfMetricsDisabled:         getenvInt("SPANBARN_SELF_METRICS_DISABLED", 0) != 0,
		BugBarnEndpoint:             os.Getenv("SPANBARN_BUGBARN_ENDPOINT"),
		BugBarnAPIKey:               os.Getenv("SPANBARN_BUGBARN_API_KEY"),
		FunnelBarnEndpoint:          os.Getenv("SPANBARN_FUNNELBARN_ENDPOINT"),
		FunnelBarnAPIKey:            os.Getenv("SPANBARN_FUNNELBARN_API_KEY"),
		FunnelBarnProject:           getenv("SPANBARN_FUNNELBARN_PROJECT", "spanbarn"),
		Environment:                 getenv("SPANBARN_ENVIRONMENT", "development"),
		LoginRatePerMinute:          getenvInt("SPANBARN_LOGIN_RATE_PER_MINUTE", 10),
		IngestRatePerMinute:         getenvInt("SPANBARN_INGEST_RATE_PER_MINUTE", 1000),
		APIRatePerMinute:            getenvInt("SPANBARN_API_RATE_PER_MINUTE", 300),
		MetricsToken:                os.Getenv("SPANBARN_METRICS_TOKEN"),
		QueryTimeoutSeconds:         getenvInt("SPANBARN_QUERY_TIMEOUT_SECONDS", 30),
		RedisURL:                    os.Getenv("SPANBARN_REDIS_URL"),
		RedisQueueURL:               os.Getenv("SPANBARN_REDIS_QUEUE_URL"),
		CacheTTLSeconds:             getenvInt("SPANBARN_CACHE_TTL_SECONDS", 30),
		Mode:                        getenv("SPANBARN_MODE", "standalone"),
		WriterURL:                   os.Getenv("SPANBARN_WRITER_URL"),
		OIDCIssuer:                  os.Getenv("SPANBARN_OIDC_ISSUER"),
		OIDCClientID:                os.Getenv("SPANBARN_OIDC_CLIENT_ID"),
		OIDCClientSecret:            os.Getenv("SPANBARN_OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:             os.Getenv("SPANBARN_OIDC_REDIRECT_URL"),
		OIDCRequiredGroup:           getenv("SPANBARN_OIDC_REQUIRED_GROUP", "spanbarn-users"),
		OIDCCLIClientID:             os.Getenv("SPANBARN_OIDC_CLI_CLIENT_ID"),
		GRPCAddr:                    getenv("SPANBARN_GRPC_ADDR", ":4317"),
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
				cfg.OIDCResourceAudiences = append(cfg.OIDCResourceAudiences, trimmed)
			}
		}
	}

	return cfg
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

func getenvFloat(key string, fallback float64) float64 {
	if raw := os.Getenv(key); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 0 && parsed <= 1 {
			return parsed
		}
	}
	return fallback
}
