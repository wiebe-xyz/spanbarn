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

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/alert"
	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/retention"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
	"github.com/wiebe-xyz/spanbarn/internal/service"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
	"github.com/wiebe-xyz/spanbarn/internal/worker"
)

// registerAuthRoutes wires the login route (rate-limited with a per-account
// throttle and a post-login cache warm) and the logout route onto mux. Shared
// by every serving mode.
func registerAuthRoutes(mux *http.ServeMux, cfg config.Config, userAuth *auth.UserAuthenticator, sessions *api.SessionService, querySvc *service.QueryService, logger *slog.Logger) {
	loginLimiter := api.NewRateLimiter(cfg.LoginRatePerMinute, cfg.IngestRatePerMinute, cfg.APIRatePerMinute)
	loginRL := api.RateLimitMiddleware(loginLimiter, "login")
	mux.Handle("/api/v1/login", loginRL(api.HandleLogin(userAuth, sessions, loginLimiter, func() {
		api.WarmLoginCaches(context.Background(), querySvc, logger)
	})))
	mux.Handle("/api/v1/logout", api.HandleLogout(sessions))
}

// runStandalone is the all-in-one single-node mode (docker-compose, small
// self-hosted installs). No Redis queue required. Reads go to a dedicated
// read-only DB connection so the writer goroutines are never starved by
// dashboard queries.
func runStandalone(cfg config.Config, logger *slog.Logger) error {
	if cfg.SessionSecret == "" {
		slog.Warn("SPANBARN_SESSION_SECRET is not set; per-project setup keys will not be stable across restarts")
	}

	// Write DB — MaxOpenConns(1), used exclusively by worker/retention/aggregation/alerts.
	store, repo, err := openWriteRepo(cfg, logger)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := applySeedKeys(repo, cfg, logger); err != nil {
		return err
	}

	// Read-only DB — used exclusively by the query service for dashboard reads.
	// In WAL mode, readers and the single writer don't block each other.
	roDB, queryRepo, err := openQueryRepo(cfg)
	if err != nil {
		return err
	}
	defer roDB.Close()

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

	ingestQueue := ingest.NewQueue(32768)
	ingestHandler := ingest.NewHandler(ingestQueue, eventSpool, 5*time.Millisecond, logger)

	metricsHandler := ingest.NewMetricsHandler(repo, logger)
	logsHandler := ingest.NewLogsHandler(repo, logger)

	// Fold every ingested metric data point into downsampled rollups so
	// long-range queries don't scan the raw metrics table.
	metricAccumulator := aggregation.NewMetricAccumulator(repo, parseAggregationInterval(cfg.AggregationInterval), 30*time.Second, logger)
	metricsHandler.SetRollupSink(metricAccumulator)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ingestHandler.Start(ctx)

	var wg sync.WaitGroup

	metricsCtx, metricsCancel := context.WithCancel(ctx)
	defer metricsCancel()
	safeGo("metrics-ingest", &wg, func() { metricsHandler.Run(metricsCtx) })
	safeGo("metric-accumulator", &wg, func() { metricAccumulator.Run(metricsCtx) })

	logsCtx, logsCancel := context.WithCancel(ctx)
	defer logsCancel()
	safeGo("logs-ingest", &wg, func() { logsHandler.Run(logsCtx) })

	aggInterval := parseAggregationInterval(cfg.AggregationInterval)
	aggregator := aggregation.NewAggregator(repo, aggInterval, logger)

	w := worker.NewWorker(eventSpool, &workerRepoAdapter{repo: repo}, logger)
	w.SetAggregator(aggregator)
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	safeGo("worker", &wg, func() { w.Run(workerCtx) })
	// Run the app-side checkpoint on a fixed interval. Its busy_timeout(30000)
	// lets it wait for a clear write window. Combined (spool) mode has no Redis
	// write-queue backlog to gate on, so no busy skip.
	startCheckpoints(workerCtx, &wg, store, logger)

	warnObsoleteRetentionEnv(cfg, logger)
	retentionCfg := retentionConfigFrom(cfg)
	repo.SetDeleteBatchYield(time.Duration(cfg.Retention.DeleteBatchYieldMS) * time.Millisecond)
	retentionWorker := retention.NewRetentionWorker(repo, aggregator, retentionCfg, logger)
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	defer retentionCancel()
	safeGo("retention", &wg, func() { retentionWorker.Run(retentionCtx) })

	compactor := newRollupCompactor(repo, cfg, logger)
	safeGo("rollup-compactor", &wg, func() { compactor.Run(retentionCtx) })

	ratioLookup := ingest.NewCachedRatioLookup(queryRepo, time.Minute)

	alertNotifier := alert.NewDefaultNotifier(alert.NotifierConfig{}, logger)
	// Alert reads (ListAlerts/QueryAggregates/QueryMetricRollups, every interval)
	// run on the read-only connection so they never contend with the single
	// writer connection; the rare trigger write stays on the writable repo.
	alertEval := alert.NewEvaluator(queryRepo, alertNotifier, logger, ratioLookup)
	alertEval.SetTriggerWriter(repo)
	alertRunner := alert.NewRunner(alertEval, queryRepo, time.Minute, logger)
	// SLO counting scans spans on the read-only connection; counts and burn
	// state are written on the writable repo.
	alertRunner.SetSLOEvaluator(alert.NewSLOEvaluator(queryRepo, repo, alertNotifier, logger, ratioLookup))
	alertCtx, alertCancel := context.WithCancel(ctx)
	defer alertCancel()
	safeGo("alert-runner", &wg, func() { alertRunner.Run(alertCtx) })

	authorizer := auth.NewAuthorizer(staticKeyHash(cfg), &keyLookupAdapter{repo: repo}, logger)
	_ = authorizer
	userAuth := auth.NewUserAuthenticator(&userLookupAdapter{repo: repo}, logger)
	sessions := newSessionService(repo, cfg, logger)

	// Query service reads from the read-only DB, never contesting the write connection.
	querySvc := service.NewQueryService(queryRepo, logger, ratioLookup)

	queryCache := newQueryCache(cfg, logger, true)
	querySvc.SetCache(queryCache)
	defer queryCache.Close()

	serverCfg := serverConfigFrom(cfg)
	serverCfg.MetricsToken = cfg.MetricsToken
	// Trace buffer: same tail-based sampling the reader applies. Without it,
	// single-node installs silently ingest everything unsampled and every
	// ingest.sample_ratio.* setting reads back fine while doing nothing.
	standaloneBuffer := newTraceBuffer(cfg, ingest.NewCachedRatioLookup(queryRepo, time.Minute), logger)
	drainTraceBuffer(ctx, &wg, standaloneBuffer, ingestHandler)
	// Mutations (trace exclusions, alerts CRUD) still use the write repo.
	apiServer := api.NewServerWithQuery(serverCfg, ingestHandler, querySvc, sessions, logger,
		api.WithRepository(repo),
		api.WithAuthorizer(authorizer),
		api.WithTraceBuffer(standaloneBuffer),
		api.WithPaths(cfg.DBPath, cfg.SpoolDir),
		api.WithCache(querySvc.Cache()),
		api.WithMetricsHandler(metricsHandler),
		api.WithLogsHandler(logsHandler),
	)
	apiServer.RegisterWorkerCounters(func() (int64, int64) {
		processed, failed, _ := w.GetMetrics()
		return processed, failed
	})
	if oidcClient := buildOIDCClient(cfg, logger); oidcClient != nil {
		apiServer.SetOIDCClient(oidcClient)
	}

	// Self-metrics: SpanBarn reports its own OTLP metrics for dogfooding.
	selfRec := selfmetrics.NewRecorder()
	selfRec.RegisterGauge("spanbarn.spool.bytes", map[string]string{"dir": cfg.SpoolDir}, func() float64 {
		return float64(eventSpool.Size())
	})
	registerTraceBufferGauges(selfRec, standaloneBuffer)
	metricAccumulator.SetOnPersist(selfRec.AddRollups)
	apiServer.SetSelfMetricsRecorder(selfRec)
	startSelfMetrics(ctx, cfg, &wg, selfRec, logger)

	api.WarmCaches(ctx, queryRepo, querySvc.Cache(), logger)

	mux := http.NewServeMux()
	registerAuthRoutes(mux, cfg, userAuth, sessions, querySvc, logger)
	mux.Handle("/", apiServer.Handler())

	httpServer := newHTTPServer(cfg, mux)
	errCh := listenAsync(httpServer, logger, "listening")

	startGRPC(ctx, cfg, &wg, apiServer, logger)

	if done, err := waitForShutdown(ctx, errCh, logger, "shutting down"); done {
		return err
	}
	shutdownHTTP(httpServer, logger, "http server shutdown error")

	alertCancel()
	retentionCancel()
	ingestHandler.Stop()
	workerCancel()
	wg.Wait()
	store.FinalCheckpoint(logger)

	logger.Info("shutdown complete")
	return nil
}
