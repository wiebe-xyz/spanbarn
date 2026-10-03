package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/forward"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

// runIngestMode starts the binary in ingest-only mode: accepts spans, buffers
// to spool, and forwards batches to the writer pod. Also serves the read API
// from the same DB (read-only) so reads survive a writer pod restart and the
// dashboard stays alive during deploys.
func runIngestMode(cfg config.Config, logger *slog.Logger) error {
	if cfg.WriterURL == "" {
		return fmt.Errorf("SPANBARN_WRITER_URL is required in ingest mode")
	}

	logger.Info("starting in ingest mode", "writer_url", cfg.WriterURL)

	eventSpool, err := spool.NewSpool(cfg.SpoolDir, cfg.MaxSpoolBytes)
	if err != nil {
		return fmt.Errorf("create spool: %w", err)
	}
	defer eventSpool.Close()

	queue := ingest.NewQueue(32768)
	ingestHandler := ingest.NewHandler(queue, eventSpool, 5*time.Millisecond, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ingestHandler.Start(ctx)

	var wg sync.WaitGroup
	fwd := forward.New(eventSpool, cfg.WriterURL, cfg.APIKey, logger)
	safeGo("forwarder", &wg, func() { fwd.Run(ctx) })

	// Open the writer's DB read-only so reads can be served when the writer
	// pod is restarting. Mutation handlers will hit SQLite "readonly database"
	// errors and return 5xx, which is the same as if the writer were down.
	var (
		roRepo    *repository.Repository
		keyLookup auth.KeyLookup
	)
	if cfg.DBPath != "" {
		db, dbErr := repository.NewReadOnlyDBWithCache(cfg.DBPath, cfg.SQLiteROCacheMB, cfg.SQLiteROMmapMB)
		if dbErr != nil {
			logger.Warn("read-only DB unavailable, API key validation limited to static key", "error", dbErr)
		} else {
			defer db.Close()
			roRepo = repository.NewReadOnlyRepository(db.DB)
			if cfg.QueryTimeoutSeconds > 0 {
				roRepo.SetQueryTimeout(time.Duration(cfg.QueryTimeoutSeconds) * time.Second)
			}
			keyLookup = newReadOnlyKeyLookup(roRepo, nil, logger)
			logger.Info("read-only DB attached for failover reads", "path", cfg.DBPath)
		}
	}
	authorizer := auth.NewAuthorizer(staticKeyHash(cfg), keyLookup, logger)

	var (
		querySvc   *service.QueryService
		sessions   *api.SessionService
		userAuth   *auth.UserAuthenticator
		queryCache *cache.Cache
	)
	if roRepo != nil {
		sessions = newSessionService(roRepo, cfg, logger)
		userAuth = auth.NewUserAuthenticator(&userLookupAdapter{repo: roRepo}, logger)
		querySvc = service.NewQueryService(roRepo, logger, ingest.NewCachedRatioLookup(roRepo, time.Minute))
		queryCache = newQueryCache(cfg, logger, false)
		querySvc.SetCache(queryCache)
		defer queryCache.Close()
	}

	serverCfg := serverConfigFrom(cfg)
	// Trace buffer: same tail-based sampling the reader applies. Without it this
	// mode silently ingests everything unsampled, ignoring every
	// ingest.sample_ratio.* setting — the mode is documented in the README, so
	// it must not quietly behave differently from reader mode.
	var ingestRatioLookup ingest.SampleRatioLookup
	if roRepo != nil {
		ingestRatioLookup = ingest.NewCachedRatioLookup(roRepo, time.Minute)
	}
	traceBuffer := newTraceBuffer(cfg, ingestRatioLookup, logger)
	drainTraceBuffer(ctx, &wg, traceBuffer, ingestHandler)

	opts := []api.ServerOption{api.WithAuthorizer(authorizer), api.WithTraceBuffer(traceBuffer)}
	if roRepo != nil {
		opts = append(opts, api.WithRepository(roRepo), api.WithPaths(cfg.DBPath, cfg.SpoolDir), api.WithCache(queryCache),
			api.WithAdmission(newAdmission(roRepo, cfg, logger)))
	}
	apiServer := api.NewServerWithQuery(serverCfg, ingestHandler, querySvc, sessions, logger, opts...)
	if oidcClient := buildOIDCClient(cfg, logger); oidcClient != nil {
		apiServer.SetOIDCClient(oidcClient)
	}

	if roRepo != nil && queryCache != nil {
		api.WarmCaches(ctx, roRepo, queryCache, logger)
	}

	mux := http.NewServeMux()
	if sessions != nil && userAuth != nil {
		registerAuthRoutes(mux, cfg, userAuth, sessions, querySvc, logger)
	}
	mux.Handle("/", apiServer.Handler())

	httpServer := newHTTPServer(cfg, mux)
	errCh := listenAsync(httpServer, logger, "ingest listening")

	if done, err := waitForShutdown(ctx, errCh, logger, "shutting down ingest"); done {
		return err
	}
	shutdownHTTP(httpServer, logger, "http server shutdown error")

	ingestHandler.Stop()
	wg.Wait()
	logger.Info("ingest shutdown complete")
	return nil
}
