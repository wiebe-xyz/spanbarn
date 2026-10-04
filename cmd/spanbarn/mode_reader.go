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
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
	"github.com/wiebe-xyz/spanbarn/internal/service"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

// runReaderMode accepts OTLP spans, serves the read-only dashboard API, and
// publishes batches to the Redis write queue. Multiple reader pods can run in
// parallel; the single writer pod drains the queue.
func runReaderMode(cfg config.Config, logger *slog.Logger) error {
	if cfg.RedisQueueURL == "" {
		return fmt.Errorf("SPANBARN_REDIS_QUEUE_URL is required in reader mode")
	}

	logger.Info("starting in reader mode", "redis_queue", cfg.RedisQueueURL)

	readerCtx, readerStop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer readerStop()

	logger.Info("connecting to write queue (retrying until ready)", "url", cfg.RedisQueueURL)
	writeQueue, err := queue.NewRedisQueueWithRetry(readerCtx, cfg.RedisQueueURL)
	if err != nil {
		return fmt.Errorf("connect to write queue: %w", err)
	}
	defer writeQueue.Close()
	logger.Info("write queue connected")

	eventSpool, err := spool.NewSpool(cfg.SpoolDir, cfg.MaxSpoolBytes)
	if err != nil {
		return fmt.Errorf("create spool: %w", err)
	}
	defer eventSpool.Close()

	ingestQueue := ingest.NewQueue(32768)
	ingestHandler := ingest.NewHandler(ingestQueue, eventSpool, 5*time.Millisecond, logger)

	ingestHandler.Start(readerCtx)

	metricsPublisher := queue.NewMetricsPublisher(writeQueue)
	readerMetricsHandler := ingest.NewMetricsHandler(metricsPublisher, logger)

	logsPublisher := queue.NewLogsPublisher(writeQueue)
	readerLogsHandler := ingest.NewLogsHandler(logsPublisher, logger)

	var wg sync.WaitGroup
	fwd := forward.NewRedisForwarder(eventSpool, writeQueue, logger)
	safeGo("redis-forwarder", &wg, func() { fwd.Run(readerCtx) })
	safeGo("metrics-ingest", &wg, func() { readerMetricsHandler.Run(readerCtx) })
	safeGo("logs-ingest", &wg, func() { readerLogsHandler.Run(readerCtx) })

	var (
		roRepo     *repository.Repository
		keyLookup  auth.KeyLookup
		querySvc   *service.QueryService
		sessions   *api.SessionService
		userAuth   *auth.UserAuthenticator
		queryCache *cache.Cache
	)
	if cfg.DBPath != "" {
		db, dbErr := openReadDB(cfg)
		if dbErr != nil {
			logger.Warn("read-only DB unavailable, dashboard reads disabled", "error", dbErr)
		} else {
			defer db.Close()
			roRepo = repository.NewReadOnlyRepository(db.DB)
			if cfg.QueryTimeoutSeconds > 0 {
				roRepo.SetQueryTimeout(time.Duration(cfg.QueryTimeoutSeconds) * time.Second)
			}
			keyLookup = newReadOnlyKeyLookup(roRepo, writeQueue, logger)

			sessions = newSessionService(roRepo, cfg, logger)
			userAuth = auth.NewUserAuthenticator(&userLookupAdapter{repo: roRepo}, logger)
			querySvc = service.NewQueryService(roRepo, logger, ingest.NewCachedRatioLookup(roRepo, time.Minute))

			queryCache = newQueryCache(cfg, logger, false)
			querySvc.SetCache(queryCache)
			defer queryCache.Close()

			logger.Info("read-only DB attached for dashboard", "path", cfg.DBPath)
		}
	}

	authorizer := auth.NewAuthorizer(staticKeyHash(cfg), keyLookup, logger)

	serverCfg := serverConfigFrom(cfg)
	// Trace buffer: holds spans for up to 10 min then applies ratio-based
	// sampling per (project, operation). Error traces always pass intact.
	var ratioLookup ingest.SampleRatioLookup
	if roRepo != nil {
		ratioLookup = ingest.NewCachedRatioLookup(roRepo, time.Minute)
	}
	traceBuffer := newTraceBuffer(cfg, ratioLookup, logger)
	drainTraceBuffer(readerCtx, &wg, traceBuffer, ingestHandler)

	opts := []api.ServerOption{api.WithAuthorizer(authorizer), api.WithTraceBuffer(traceBuffer), api.WithMetricsHandler(readerMetricsHandler), api.WithLogsHandler(readerLogsHandler)}
	if roRepo != nil {
		opts = append(opts, api.WithRepository(roRepo), api.WithPaths(cfg.DBPath, cfg.SpoolDir), api.WithCache(queryCache),
			api.WithAdmission(newAdmission(roRepo, cfg, logger)))
	}
	apiServer := api.NewServerWithQuery(serverCfg, ingestHandler, querySvc, sessions, logger, opts...)
	if oidcClient := buildOIDCClient(cfg, logger); oidcClient != nil {
		apiServer.SetOIDCClient(oidcClient)
	}

	// Self-metrics for reader/ingest pod: request rates, latency, spool depth, queue depth.
	readerSelfRec := selfmetrics.NewRecorder()
	readerSelfRec.RegisterGauge("spanbarn.spool.bytes", map[string]string{"dir": cfg.SpoolDir}, func() float64 {
		return float64(eventSpool.Size())
	})
	registerQueueDepthGauges(readerCtx, readerSelfRec, writeQueue)
	registerTraceBufferGauges(readerSelfRec, traceBuffer)
	apiServer.SetSelfMetricsRecorder(readerSelfRec)
	startSelfMetrics(readerCtx, cfg, &wg, readerSelfRec, logger)

	startGRPC(readerCtx, cfg, &wg, apiServer, logger)

	if roRepo != nil && queryCache != nil {
		api.WarmCaches(readerCtx, roRepo, queryCache, logger)
	}

	mux := http.NewServeMux()
	if sessions != nil && userAuth != nil {
		registerAuthRoutes(mux, cfg, userAuth, sessions, querySvc, logger)
	}
	mux.Handle("/", apiServer.Handler())

	httpServer := newHTTPServer(cfg, mux)
	errCh := listenAsync(httpServer, logger, "reader listening")

	if done, err := waitForShutdown(readerCtx, errCh, logger, "shutting down reader"); done {
		return err
	}
	shutdownHTTP(httpServer, logger, "reader shutdown error")

	ingestHandler.Stop()
	wg.Wait()
	logger.Info("reader shutdown complete")
	return nil
}
