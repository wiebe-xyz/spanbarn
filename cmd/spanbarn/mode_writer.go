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
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/retention"
	"github.com/wiebe-xyz/spanbarn/internal/sampling"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
	"github.com/wiebe-xyz/spanbarn/internal/service"
	"github.com/wiebe-xyz/spanbarn/internal/worker"
)

// runWriterMode drains the Redis write queue, writes spans to SQLite, and runs
// background workers (aggregation, retention, alerts). It also serves the full
// mutation API (POST/PUT/DELETE) and login so that the Traefik ingress rule
// that routes writes to the spanbarn service continues to work.
//
// Startup order:
//  1. Health endpoint starts immediately (k8s probes pass during migrations)
//  2. DB open + migrations
//  3. Read-only DB for query service (reads don't block the write connection)
//  4. Full API server wired into the same mux (mutations + login now available)
//  5. Redis queue connect + workers
func runWriterMode(cfg config.Config, logger *slog.Logger) error {
	if cfg.RedisQueueURL == "" {
		return fmt.Errorf("SPANBARN_REDIS_QUEUE_URL is required in writer mode")
	}

	logger.Info("starting in writer mode", "redis_queue", cfg.RedisQueueURL)

	if cfg.SessionSecret == "" {
		slog.Warn("SPANBARN_SESSION_SECRET is not set; per-project setup keys will not be stable across restarts")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Step 1: health endpoint up immediately so startup probes pass during migrations.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","mode":"writer"}`)
	})

	httpServer := newHTTPServer(cfg, mux)
	httpErrCh := listenAsync(httpServer, logger, "writer listening")

	// Step 2: write DB — MaxOpenConns(1), used by worker/retention/aggregation/alerts.
	store, repo, err := openWriteRepo(cfg, logger)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := applySeedKeys(repo, cfg, logger); err != nil {
		return err
	}

	if cfg.AdminUsername != "" && cfg.AdminPassword != "" {
		if err := bootstrapAdmin(repo, cfg, logger); err != nil {
			return err
		}
	}

	// Step 3: read-only DB for the query service — reads don't compete with writes.
	roDB, queryRepo, err := openQueryRepo(cfg)
	if err != nil {
		return err
	}
	defer roDB.Close()

	// Step 4: full API server wired into the already-listening mux.
	authorizer := auth.NewAuthorizer(staticKeyHash(cfg), &keyLookupAdapter{repo: repo}, logger)
	userAuth := auth.NewUserAuthenticator(&userLookupAdapter{repo: repo}, logger)
	sessions := newSessionService(repo, cfg, logger)

	writerRatioLookup := ingest.NewCachedRatioLookup(queryRepo, time.Minute)
	querySvc := service.NewQueryService(queryRepo, logger, writerRatioLookup)
	queryCache := newQueryCache(cfg, logger, false)
	querySvc.SetCache(queryCache)
	defer queryCache.Close()

	serverCfg := serverConfigFrom(cfg)
	serverCfg.MetricsToken = cfg.MetricsToken
	// No ingest handler — OTLP goes to the reader pod per ingress rules.
	apiServer := api.NewServerWithQuery(serverCfg, nil, querySvc, sessions, logger,
		api.WithRepository(repo),
		api.WithAuthorizer(authorizer),
		api.WithPaths(cfg.DBPath, cfg.SpoolDir),
		api.WithCache(querySvc.Cache()),
	)
	if oidcClient := buildOIDCClient(cfg, logger); oidcClient != nil {
		apiServer.SetOIDCClient(oidcClient)
	}
	registerAuthRoutes(mux, cfg, userAuth, sessions, querySvc, logger)
	mux.Handle("/", apiServer.Handler())
	logger.Info("writer API ready")

	// Self-metrics recorder wired to the API server now; gauges and reporter are
	// started after the write queue and metric accumulator are available (step 5).
	writerSelfRec := selfmetrics.NewRecorder()
	apiServer.SetSelfMetricsRecorder(writerSelfRec)

	// Step 5: Redis queue connect + workers.
	logger.Info("connecting to write queue (retrying until ready)", "url", cfg.RedisQueueURL)
	writeQueue, err := queue.NewRedisQueueWithRetry(ctx, cfg.RedisQueueURL)
	if err != nil {
		return fmt.Errorf("connect to write queue: %w", err)
	}
	defer writeQueue.Close()
	logger.Info("write queue connected")

	var wg sync.WaitGroup

	aggInterval := parseAggregationInterval(cfg.AggregationInterval)
	accumulator := aggregation.NewAccumulator(repo, aggInterval, 30*time.Second, logger)
	metricAccumulator := aggregation.NewMetricAccumulator(repo, aggInterval, 30*time.Second, logger)

	// Complete self-metrics wiring: rollup callback + queue depth gauges + reporter.
	metricAccumulator.SetOnPersist(writerSelfRec.AddRollups)
	registerQueueDepthGauges(ctx, writerSelfRec, writeQueue)
	startSelfMetrics(ctx, cfg, &wg, writerSelfRec, logger)

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	startWriteSchedulers(workerCtx, &wg, store, repo, logger)

	boringPolicy := worker.NewCachedBoringPolicy(repo, 30*time.Second)

	// The writer is the single SQLite writer, so an in-memory per-minute floor
	// counts boring-trace survivals accurately across batches.
	minuteFloor := sampling.NewMinuteFloor()
	hourFloor := sampling.NewBucketFloor(time.Hour)

	rw := worker.NewRedisWorker(writeQueue, &workerRepoAdapter{repo: repo}, logger)
	rw.SetAccumulator(accumulator)
	rw.SetConfig(worker.WorkerConfig{
		SlowThresholdUs: int64(cfg.SlowThresholdMS) * 1000,
		BoringRetention: time.Duration(cfg.Retention.BoringMinutes) * time.Minute,
	})
	rw.SetBoringPolicy(boringPolicy)
	rw.SetMinuteFloor(minuteFloor)
	rw.SetHourFloor(hourFloor)
	rw.SetStagingMode(cfg.SpanStagingEnabled)
	safeGo("redis-worker", &wg, func() { rw.Run(workerCtx) })

	// Span staging (opt-in): the redis worker only appends to spans_staging; this
	// flusher does accumulation + classification + indexed storage per complete
	// trace off the hot path, with a hard-age GC so staging can't grow unbounded.
	if cfg.SpanStagingEnabled {
		flusher := newStagingFlusher(cfg, queryRepo, repo, accumulator, boringPolicy, minuteFloor, hourFloor, logger)
		safeGo("staging-flusher", &wg, func() { flusher.Run(workerCtx) })
	}
	safeGo("accumulator", &wg, func() { accumulator.Run(workerCtx) })
	safeGo("metric-accumulator", &wg, func() { metricAccumulator.Run(workerCtx) })
	safeGo("metrics-consumer", &wg, func() { runMetricsConsumer(workerCtx, writeQueue, metricAccumulator, repo, logger) })
	safeGo("logs-consumer", &wg, func() { runLogsConsumer(workerCtx, writeQueue, repo, logger) })
	safeGo("apikey-touch-consumer", &wg, func() { runTouchConsumer(workerCtx, writeQueue, repo, logger) })
	startCheckpoints(workerCtx, &wg, store, logger)

	// Retention queries (DELETE/SELECT on the full spans table) can take
	// several minutes when the backlog is large. Give it a copy of repo with a
	// longer timeout so individual queries don't abort mid-cycle. The copy
	// shares repo's handles and write schedulers, so all writes are still
	// serialised and prioritised per file.
	retentionRepo := repo.WithQueryTimeout(5 * time.Minute)
	retentionRepo.SetDeleteBatchYield(time.Duration(cfg.Retention.DeleteBatchYieldMS) * time.Millisecond)

	warnObsoleteRetentionEnv(cfg, logger)
	retentionCfg := retentionConfigFrom(cfg)
	retentionWorker := retention.NewRetentionWorker(retentionRepo, accumulator, retentionCfg, logger)
	registerStorageMetrics(writerSelfRec, retentionWorker.Stats)
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	defer retentionCancel()
	safeGo("retention", &wg, func() { retentionWorker.Run(retentionCtx) })

	// Shares retentionRepo for the same reason retention does: a longer query
	// timeout and the write scheduler, on the one writer connection.
	compactor := newRollupCompactor(retentionRepo, cfg, logger)
	safeGo("rollup-compactor", &wg, func() { compactor.Run(retentionCtx) })

	alertNotifier := alert.NewDefaultNotifier(alert.NotifierConfig{}, logger)
	// Alert reads run on the read-only connection; the rare trigger write stays
	// on the writable repo. Keeps alert evaluation off the single writer path.
	alertEval := alert.NewEvaluator(queryRepo, alertNotifier, logger, writerRatioLookup)
	alertEval.SetTriggerWriter(repo)
	alertRunner := alert.NewRunner(alertEval, queryRepo, time.Minute, logger)
	// SLO counting scans spans on the read-only connection; counts and burn
	// state are written on the writable repo.
	alertRunner.SetSLOEvaluator(alert.NewSLOEvaluator(queryRepo, repo, alertNotifier, logger, writerRatioLookup))
	alertCtx, alertCancel := context.WithCancel(ctx)
	defer alertCancel()
	safeGo("alert-runner", &wg, func() { alertRunner.Run(alertCtx) })

	querySvc.SetAccumulator(accumulator)
	api.WarmCaches(ctx, queryRepo, querySvc.Cache(), logger)

	if done, err := waitForShutdown(ctx, httpErrCh, logger, "shutting down writer"); done {
		return err
	}
	shutdownHTTP(httpServer, logger, "writer shutdown error")

	alertCancel()
	retentionCancel()
	workerCancel()
	wg.Wait()
	store.FinalCheckpoint(logger)

	logger.Info("writer shutdown complete")
	return nil
}
