package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/observability"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
	"golang.org/x/crypto/bcrypt"
)

// buildOIDCClient returns an OIDC adapter when all four SPANBARN_OIDC_* vars
// are set, or nil otherwise (in which case the local single-user login is the
// only auth path). Discovery is lazy so an unreachable issuer at startup does
// not crash the process.
func buildOIDCClient(cfg config.Config, logger *slog.Logger) *auth.OIDCClient {
	oc := auth.OIDCConfig{
		Issuer:                cfg.OIDC.Issuer,
		ClientID:              cfg.OIDC.ClientID,
		ClientSecret:          cfg.OIDC.ClientSecret,
		RedirectURL:           cfg.OIDC.RedirectURL,
		RequiredGroup:         cfg.OIDC.RequiredGroup,
		ResourceAudiences:     cfg.OIDC.ResourceAudiences,
		CLIClientID:           cfg.OIDC.CLIClientID,
		PostLogoutRedirectURI: cfg.OIDC.PostLogoutRedirectURI,
	}
	// The sb CLI's device-code tokens carry the CLI client id as their
	// audience, so accept it as a resource audience automatically.
	if cfg.OIDC.CLIClientID != "" {
		oc.ResourceAudiences = append(oc.ResourceAudiences, cfg.OIDC.CLIClientID)
	}
	if !oc.Enabled() {
		return nil
	}
	logger.Info("oidc: enabled", "issuer", oc.Issuer, "client_id", oc.ClientID, "required_group", oc.RequiredGroup, "resource_audiences", oc.ResourceAudiences)
	return auth.NewOIDCClient(oc)
}

func bootstrapAdmin(repo *repository.Repository, cfg config.Config, logger *slog.Logger) error {
	hash, hashErr := auth.HashPassword(cfg.AdminPassword)
	if hashErr != nil {
		return fmt.Errorf("hash admin password: %w", hashErr)
	}
	existing, err := repo.GetUserByUsername(cfg.AdminUsername)
	if err != nil {
		if createErr := repo.CreateUser(cfg.AdminUsername, hash); createErr != nil {
			return fmt.Errorf("create admin user: %w", createErr)
		}
		logger.Info("bootstrapped admin user", "username", cfg.AdminUsername)
	} else if bcrypt.CompareHashAndPassword([]byte(existing.PasswordHash), []byte(cfg.AdminPassword)) != nil {
		if updateErr := repo.UpdateUserPassword(cfg.AdminUsername, hash); updateErr != nil {
			return fmt.Errorf("update admin password: %w", updateErr)
		}
		logger.Info("updated admin password from env", "username", cfg.AdminUsername)
	}
	return nil
}

func safeGo(name string, wg *sync.WaitGroup, fn func()) {
	observability.SafeGo(name, wg, fn)
}

// startSelfMetrics wires and launches the periodic self-metrics reporter, which
// POSTs SpanBarn's own OTLP metrics to its ingest endpoint so the Metrics page
// always has live data (dogfooding). A nil rec or disabled config is a no-op.
func startSelfMetrics(ctx context.Context, cfg config.Config, wg *sync.WaitGroup, rec *selfmetrics.Recorder, logger *slog.Logger) {
	if rec == nil || cfg.Self.MetricsDisabled {
		return
	}
	endpoint := cfg.Self.Endpoint
	apiKey := cfg.Self.APIKey
	if endpoint == "" {
		// In standalone mode (no explicit endpoint) post to ourselves so the
		// feature works out of the box without extra env vars.
		addr := cfg.Addr
		if len(addr) > 0 && addr[0] == ':' {
			addr = "127.0.0.1" + addr
		}
		endpoint = "http://" + addr
	}
	if apiKey == "" {
		apiKey = cfg.APIKey
	}
	if endpoint == "" || apiKey == "" {
		logger.Info("self-metrics disabled (no endpoint or API key configured)")
		return
	}
	startNano := uint64(time.Now().UnixNano())
	reporter := selfmetrics.NewReporter(
		rec,
		endpoint, apiKey,
		time.Duration(cfg.Self.MetricsIntervalSec)*time.Second,
		map[string]string{
			"service.name":     "spanbarn",
			"spanbarn.mode":    cfg.Mode,
			"spanbarn.version": Version,
		},
		startNano,
		logger,
	)
	logger.Info("self-metrics enabled", "endpoint", endpoint, "interval_s", cfg.Self.MetricsIntervalSec)
	safeGo("self-metrics", wg, func() { reporter.Run(ctx) })
}

func parseAggregationInterval(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return time.Minute
	}
	return d
}

// --- Adapters to bridge repository types to package-specific interfaces ---

type workerRepoAdapter struct {
	repo *repository.Repository
}

func (a *workerRepoAdapter) InsertSpans(ctx context.Context, spans []repository.Span) error {
	return a.repo.InsertSpansContext(ctx, spans)
}

func (a *workerRepoAdapter) InsertSpansStaging(ctx context.Context, spans []repository.Span) error {
	return a.repo.InsertSpansStaging(ctx, spans)
}

func (a *workerRepoAdapter) InsertPromptRecords(_ context.Context, records []repository.PromptRecord) error {
	return a.repo.InsertPromptRecords(records)
}

type userLookupAdapter struct {
	repo *repository.Repository
}

func (a *userLookupAdapter) GetUserByUsername(username string) (auth.UserRecord, error) {
	u, err := a.repo.GetUserByUsername(username)
	if err != nil {
		return auth.UserRecord{}, err
	}
	return auth.UserRecord{
		ID:           u.ID,
		Username:     u.Username,
		PasswordHash: u.PasswordHash,
	}, nil
}
