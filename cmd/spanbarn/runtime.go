package main

import (
	"log/slog"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/retention"
)

// serverConfigFrom builds the api.ServerConfig fields shared by every mode.
// MetricsToken is deliberately left unset: the standalone and writer modes opt
// into token-gated /metrics by setting it on the returned value, while reader
// and ingest historically leave it open — so callers set it explicitly rather
// than have this helper change that behaviour.
func serverConfigFrom(cfg config.Config) api.ServerConfig {
	return api.ServerConfig{
		APIKey:             cfg.APIKey,
		MaxBodyBytes:       cfg.MaxBodyBytes,
		AllowedOrigins:     cfg.AllowedOrigins,
		Version:            Version,
		Environment:        cfg.Environment,
		LoginRate:          cfg.LoginRatePerMinute,
		IngestRate:         cfg.IngestRatePerMinute,
		APIRate:            cfg.APIRatePerMinute,
		SessionSecret:      cfg.SessionSecret,
		PublicURL:          cfg.PublicURL,
		FunnelBarnEndpoint: cfg.FunnelBarn.Endpoint,
		FunnelBarnAPIKey:   cfg.FunnelBarn.APIKey,
		FunnelBarnProject:  cfg.FunnelBarn.Project,
		E2EEnabled:         cfg.E2EEnabled,
	}
}

// newSessionService builds the token-bound web-session service over the given
// repository. Returns nil when this mode has no database access at all, in
// which case no session-authenticated route works (matching the old
// behaviour of a nil session manager). On read-only repositories (reader /
// ingest pods) the service validates sessions but never runs the refresh
// grant — rotations happen on the writer via POST /api/v1/session/refresh.
func newSessionService(repo *repository.Repository, cfg config.Config, logger *slog.Logger) *api.SessionService {
	if repo == nil {
		return nil
	}
	return api.NewSessionService(repo, cfg.SessionTTLSeconds, cfg.OIDC.RefreshGraceSeconds, logger)
}

// retentionConfigFrom maps the app config's retention windows onto the retention
// worker's config. Shared by the standalone and writer bootstraps so the mapping
// lives in one place.
func retentionConfigFrom(cfg config.Config) retention.Config {
	return retention.Config{
		FullRetentionHours:        cfg.Retention.FullHours,
		InterestingRetentionHours: cfg.Retention.InterestingHours,
		BoringRetentionMinutes:    cfg.Retention.BoringMinutes,
		ErrorRetentionDays:        cfg.Retention.ErrorDays,
		AggregateRetentionDays:    cfg.Retention.AggregatedDays,
		MetricsRetentionDays:      cfg.Retention.MetricsDays,
		LogRetentionHours:         cfg.Retention.LogHours,
		ErrorLogRetentionDays:     cfg.Retention.ErrorLogDays,
		SlowThresholdUS:           int64(cfg.SlowThresholdMS) * 1000,
	}
}
