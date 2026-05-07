package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/alert"
	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/observability"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/retention"
	"github.com/wiebe-xyz/spanbarn/internal/service"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
	"github.com/wiebe-xyz/spanbarn/internal/worker"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger, shutdownObservability := observability.Setup(Version)
	slog.SetDefault(logger)
	defer shutdownObservability()

	cfg := config.Load()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("spanbarn %s (built %s)\n", Version, BuildTime)
			return nil
		case "worker-once":
			return runWorkerOnce(cfg)
		case "user":
			return runUserCmd(cfg, os.Args[2:])
		case "project":
			return runProjectCmd(cfg, os.Args[2:])
		case "apikey":
			return runAPIKeyCmd(cfg, os.Args[2:])
		}
	}

	if cfg.SessionSecret == "" {
		slog.Warn("SPANBARN_SESSION_SECRET is not set; sessions will not persist across restarts")
	}

	db, err := repository.NewDB(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if err := repository.Migrate(db.DB); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	logger.Info("storage", "path", cfg.DBPath)

	repo := repository.NewRepository(db.DB)

	if cfg.AdminUsername != "" && cfg.AdminPassword != "" {
		if err := bootstrapAdmin(repo, cfg, logger); err != nil {
			return err
		}
	}

	eventSpool, err := spool.NewSpool(cfg.SpoolDir, cfg.MaxSpoolBytes)
	if err != nil {
		return fmt.Errorf("create spool: %w", err)
	}
	defer eventSpool.Close()
	logger.Info("spool", "dir", cfg.SpoolDir)

	queue := ingest.NewQueue(32768)
	ingestHandler := ingest.NewHandler(queue, eventSpool, 5*time.Millisecond, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ingestHandler.Start(ctx)

	w := worker.NewWorker(eventSpool, &workerRepoAdapter{repo: repo}, logger)
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	go w.Run(workerCtx)

	aggInterval := parseAggregationInterval(cfg.AggregationInterval)
	aggregator := aggregation.NewAggregator(repo, aggInterval, logger)

	retentionCfg := retention.Config{
		FullRetentionHours:     cfg.RetentionFullHours,
		ErrorRetentionDays:     cfg.RetentionErrorDays,
		AggregateRetentionDays: cfg.RetentionAggregatedDays,
		SlowThresholdUS:        int64(cfg.SlowThresholdMS) * 1000,
	}
	retentionWorker := retention.NewRetentionWorker(repo, aggregator, retentionCfg, logger)
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	defer retentionCancel()
	go retentionWorker.Run(retentionCtx)

	alertNotifier := alert.NewDefaultNotifier(alert.NotifierConfig{}, logger)
	alertEval := alert.NewEvaluator(repo, alertNotifier, logger)
	alertRunner := alert.NewRunner(alertEval, repo, time.Minute, logger)
	alertCtx, alertCancel := context.WithCancel(ctx)
	defer alertCancel()
	go alertRunner.Run(alertCtx)

	authorizer := auth.NewAuthorizer(cfg.APIKeySHA256, &keyLookupAdapter{repo: repo}, logger)
	_ = authorizer
	userAuth := auth.NewUserAuthenticator(&userLookupAdapter{repo: repo}, logger)
	sessionMgr := auth.NewSessionManager(cfg.SessionSecret, int64(cfg.SessionTTLSeconds))

	querySvc := service.NewQueryService(repo, logger)

	serverCfg := api.ServerConfig{
		APIKey:         cfg.APIKey,
		MaxBodyBytes:   cfg.MaxBodyBytes,
		AllowedOrigins: cfg.AllowedOrigins,
		Version:        Version,
		MetricsToken:   cfg.MetricsToken,
		LoginRate:      cfg.LoginRatePerMinute,
		IngestRate:     cfg.IngestRatePerMinute,
		APIRate:        cfg.APIRatePerMinute,
		SessionSecret:  cfg.SessionSecret,
		PublicURL:      cfg.PublicURL,
	}
	apiServer := api.NewServerWithQuery(serverCfg, ingestHandler, querySvc, sessionMgr, logger, api.WithRepository(repo), api.WithAuthorizer(authorizer), api.WithPaths(cfg.DBPath, cfg.SpoolDir))

	mux := http.NewServeMux()
	loginRL := api.RateLimitMiddleware(api.NewRateLimiter(cfg.LoginRatePerMinute, cfg.IngestRatePerMinute, cfg.APIRatePerMinute), "login")
	mux.Handle("/api/v1/login", loginRL(api.HandleLogin(userAuth, sessionMgr)))
	mux.Handle("/api/v1/logout", http.HandlerFunc(api.HandleLogout()))
	mux.Handle("/", apiServer.Handler())

	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server shutdown error", "error", err)
	}

	alertCancel()
	retentionCancel()
	ingestHandler.Stop()
	workerCancel()

	logger.Info("shutdown complete")
	return nil
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

func (a *workerRepoAdapter) InsertSpans(_ context.Context, spans []repository.Span) error {
	return a.repo.InsertSpans(spans)
}

type keyLookupAdapter struct {
	repo *repository.Repository
}

func (a *keyLookupAdapter) GetAPIKeyByHash(keyHash string) (auth.APIKeyRecord, error) {
	k, err := a.repo.GetAPIKeyByHash(keyHash)
	if err != nil {
		return auth.APIKeyRecord{}, err
	}
	return auth.APIKeyRecord{
		ID:        k.ID,
		ProjectID: k.ProjectID,
		Scope:     k.Scope,
	}, nil
}

func (a *keyLookupAdapter) TouchAPIKey(id int64) error {
	return a.repo.TouchAPIKey(id)
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
