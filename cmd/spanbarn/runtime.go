package main

import (
	"context"
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

// warnObsoleteRetentionEnv reports SPANBARN_RETENTION_FULL_HOURS still being set.
// It named a window that no longer exists — uninteresting spans now expire via
// SPANBARN_BORING_RETENTION_MINUTES, and the span window is
// SPANBARN_RETENTION_INTERESTING_HOURS. It was silently ignored, which let an
// operator believe span retention was capped when it was not; that is how
// production's disk filled.
func warnObsoleteRetentionEnv(cfg config.Config, logger *slog.Logger) {
	if cfg.Retention.FullHours <= 0 {
		return
	}
	logger.Warn("SPANBARN_RETENTION_FULL_HOURS is obsolete and ignored — "+
		"span retention is SPANBARN_RETENTION_INTERESTING_HOURS; uninteresting spans expire via "+
		"SPANBARN_BORING_RETENTION_MINUTES. Unset it.",
		"ignored_value", cfg.Retention.FullHours,
		"retention_interesting_hours", cfg.Retention.InterestingHours,
		"boring_retention_minutes", cfg.Retention.BoringMinutes)
}

// newAdmission builds the disk-pressure admission controller for the pods that
// receive telemetry. It is the last rung of the ladder that starts in the
// retention worker (shorten windows at 75%, harder at 90%): if the volume still
// climbs to the reject threshold, refuse telemetry outright so that
// control-plane writes — above all the session insert that login depends on —
// keep working. Returns nil when there is no database to measure, which leaves
// admission disabled rather than guessing.
func newAdmission(repo *repository.Repository, cfg config.Config, logger *slog.Logger) *api.Admission {
	if repo == nil || cfg.DBPath == "" {
		return nil
	}
	probe := func(ctx context.Context) (float64, bool) {
		space, err := repo.DBSpace(ctx, cfg.DBPath)
		if err != nil || !space.Measured() {
			return 0, false
		}
		return space.UsedFraction(), true
	}
	a := api.NewAdmission(probe, float64(cfg.IngestRejectDiskPct)/100)
	if a.Enabled() {
		logger.Info("telemetry admission control enabled",
			"reject_at_disk_pct", cfg.IngestRejectDiskPct, "db_path", cfg.DBPath)
	}
	return a
}

// retentionConfigFrom maps the app config's retention windows onto the retention
// worker's config. Shared by the standalone and writer bootstraps so the mapping
// lives in one place.
func retentionConfigFrom(cfg config.Config) retention.Config {
	return retention.Config{
		InterestingRetentionHours: cfg.Retention.InterestingHours,
		BoringRetentionMinutes:    cfg.Retention.BoringMinutes,
		ErrorRetentionDays:        cfg.Retention.ErrorDays,
		AggregateRetentionDays:    cfg.Retention.AggregatedDays,
		MetricsRetentionDays:      cfg.Retention.MetricsDays,
		LogRetentionHours:         cfg.Retention.LogHours,
		ErrorLogRetentionDays:     cfg.Retention.ErrorLogDays,
		SlowThresholdUS:           int64(cfg.SlowThresholdMS) * 1000,
		DBPath:                    cfg.DBPath,
		Watermarks: retention.Watermarks{
			Elevated: float64(cfg.Retention.DiskElevatedPct) / 100,
			Critical: float64(cfg.Retention.DiskCriticalPct) / 100,
		},
	}
}
