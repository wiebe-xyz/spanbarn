package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/alert"
	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/forward"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/observability"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/retention"
	"github.com/wiebe-xyz/spanbarn/internal/sampling"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
	"github.com/wiebe-xyz/spanbarn/internal/service"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
	"github.com/wiebe-xyz/spanbarn/internal/worker"
	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
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

	if err := cfg.Validate(); err != nil {
		return err
	}

	// Trust proxy forwarding headers for client-IP determination only when
	// configured (default: on outside dev), so rate limiting keys on the real
	// client behind Caddy/Nginx instead of the shared proxy IP.
	api.SetTrustProxy(cfg.TrustProxy)

	switch cfg.Mode {
	case "ingest":
		return runIngestMode(cfg, logger)
	case "reader":
		return runReaderMode(cfg, logger)
	case "writer":
		return runWriterMode(cfg, logger)
	default:
		return runStandalone(cfg, logger)
	}
}

// runStandalone is the all-in-one single-node mode (docker-compose, small
// self-hosted installs). No Redis queue required. Reads go to a dedicated
// read-only DB connection so the writer goroutines are never starved by
// dashboard queries.
func runStandalone(cfg config.Config, logger *slog.Logger) error {
	if cfg.SessionSecret == "" {
		slog.Warn("SPANBARN_SESSION_SECRET is not set; sessions will not persist across restarts")
	}

	// Write DB — MaxOpenConns(1), used exclusively by worker/retention/aggregation/alerts.
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
	if cfg.QueryTimeoutSeconds > 0 {
		repo.SetQueryTimeout(time.Duration(cfg.QueryTimeoutSeconds) * time.Second)
	}

	// Read-only DB — used exclusively by the query service for dashboard reads.
	// In WAL mode, readers and the single writer don't block each other.
	roDB, err := repository.NewReadOnlyDB(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open read-only database: %w", err)
	}
	defer roDB.Close()
	queryRepo := repository.NewRepository(roDB.DB)
	if cfg.QueryTimeoutSeconds > 0 {
		queryRepo.SetQueryTimeout(time.Duration(cfg.QueryTimeoutSeconds) * time.Second)
	}

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
	// lets it wait for a clear write window, which Litestream's own checkpoint
	// (no busy_timeout, ~20 writes/s) rarely gets — so the writer stays the
	// reliable WAL flush. When Litestream is attached we checkpoint in PASSIVE
	// mode so we never reset its WAL generation (a TRUNCATE would force a full
	// re-snapshot); Litestream itself owns WAL truncation of the same generation.
	cpMode := checkpointMode()
	// Under Litestream (PASSIVE) the WAL grows under sustained read load; escalate
	// to a bounding TRUNCATE only once it exceeds the configured size (~256 WAL
	// frames per MiB at the default 4 KiB page size). 0 = never (standalone TRUNCATE).
	cpTruncateFrames := 0
	if cpMode == repository.CheckpointPassive {
		cpTruncateFrames = cfg.WALTruncateThresholdMB * 256
	}
	// Combined (spool) mode has no Redis write-queue backlog to gate on, so no busy skip.
	safeGo("wal-checkpoint", &wg, func() { db.RunPeriodicCheckpoint(workerCtx, 30*time.Second, cpMode, cpTruncateFrames, nil, logger) })
	if litestreamActive() {
		logger.Info("litestream active: writer runs PASSIVE WAL checkpoints; Litestream owns WAL truncation")
	}

	retentionCfg := retention.Config{
		FullRetentionHours:        cfg.RetentionFullHours,
		InterestingRetentionHours: cfg.RetentionInterestingHours,
		BoringRetentionMinutes:    cfg.BoringRetentionMinutes,
		ErrorRetentionDays:        cfg.RetentionErrorDays,
		AggregateRetentionDays:    cfg.RetentionAggregatedDays,
		MetricsRetentionDays:      cfg.MetricsRetentionDays,
		LogRetentionHours:         cfg.LogRetentionHours,
		ErrorLogRetentionDays:     cfg.ErrorLogRetentionDays,
		SlowThresholdUS:           int64(cfg.SlowThresholdMS) * 1000,
	}
	repo.SetDeleteBatchYield(time.Duration(cfg.RetentionDeleteBatchYieldMS) * time.Millisecond)
	retentionWorker := retention.NewRetentionWorker(repo, aggregator, retentionCfg, logger)
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	defer retentionCancel()
	safeGo("retention", &wg, func() { retentionWorker.Run(retentionCtx) })

	ratioLookup := ingest.NewCachedRatioLookup(queryRepo, time.Minute)

	alertNotifier := alert.NewDefaultNotifier(alert.NotifierConfig{}, logger)
	// Alert reads (ListAlerts/QueryAggregates/QueryMetricRollups, every interval)
	// run on the read-only connection so they never contend with the single
	// writer connection; the rare trigger write stays on the writable repo.
	alertEval := alert.NewEvaluator(queryRepo, alertNotifier, logger, ratioLookup)
	alertEval.SetTriggerWriter(repo)
	alertRunner := alert.NewRunner(alertEval, queryRepo, time.Minute, logger)
	alertCtx, alertCancel := context.WithCancel(ctx)
	defer alertCancel()
	safeGo("alert-runner", &wg, func() { alertRunner.Run(alertCtx) })

	apiKeyHash := cfg.APIKeySHA256
	if apiKeyHash == "" && cfg.APIKey != "" {
		apiKeyHash = auth.HashKey(cfg.APIKey)
	}
	authorizer := auth.NewAuthorizer(apiKeyHash, &keyLookupAdapter{repo: repo}, logger)
	_ = authorizer
	userAuth := auth.NewUserAuthenticator(&userLookupAdapter{repo: repo}, logger)
	sessionMgr := auth.NewSessionManager(cfg.SessionSecret, int64(cfg.SessionTTLSeconds))

	// Query service reads from the read-only DB, never contesting the write connection.
	querySvc := service.NewQueryService(queryRepo, logger, ratioLookup)

	{
		ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
		var store cache.Store
		if cfg.RedisURL != "" {
			rs, cacheErr := cache.NewRedisStore(cfg.RedisURL)
			if cacheErr != nil {
				logger.Warn("redis unavailable, falling back to in-memory cache", "error", cacheErr)
				store = cache.NewMemoryStore()
			} else {
				store = rs
				logger.Info("redis cache enabled", "ttl", ttl)
			}
		} else {
			store = cache.NewMemoryStore()
			logger.Info("in-memory cache enabled", "ttl", ttl)
		}
		queryCache := cache.New(store, ttl)
		querySvc.SetCache(queryCache)
		defer queryCache.Close()
	}

	serverCfg := api.ServerConfig{
		APIKey:             cfg.APIKey,
		MaxBodyBytes:       cfg.MaxBodyBytes,
		AllowedOrigins:     cfg.AllowedOrigins,
		Version:            Version,
		Environment:        cfg.Environment,
		MetricsToken:       cfg.MetricsToken,
		LoginRate:          cfg.LoginRatePerMinute,
		IngestRate:         cfg.IngestRatePerMinute,
		APIRate:            cfg.APIRatePerMinute,
		SessionSecret:      cfg.SessionSecret,
		PublicURL:          cfg.PublicURL,
		FunnelBarnEndpoint: cfg.FunnelBarnEndpoint,
		FunnelBarnAPIKey:   cfg.FunnelBarnAPIKey,
		FunnelBarnProject:  cfg.FunnelBarnProject,
	}
	// Mutations (trace exclusions, alerts CRUD) still use the write repo.
	apiServer := api.NewServerWithQuery(serverCfg, ingestHandler, querySvc, sessionMgr, logger,
		api.WithRepository(repo),
		api.WithAuthorizer(authorizer),
		api.WithPaths(cfg.DBPath, cfg.SpoolDir),
		api.WithCache(querySvc.Cache()),
		api.WithMetricsHandler(metricsHandler),
		api.WithLogsHandler(logsHandler),
	)
	if oidcClient := buildOIDCClient(cfg, logger); oidcClient != nil {
		apiServer.SetOIDCClient(oidcClient)
	}

	// Self-metrics: SpanBarn reports its own OTLP metrics for dogfooding.
	selfRec := selfmetrics.NewRecorder()
	selfRec.RegisterGauge("spanbarn.spool.bytes", map[string]string{"dir": cfg.SpoolDir}, func() float64 {
		return float64(eventSpool.Size())
	})
	metricAccumulator.SetOnPersist(selfRec.AddRollups)
	apiServer.SetSelfMetricsRecorder(selfRec)
	startSelfMetrics(ctx, cfg, &wg, selfRec, logger)

	api.WarmCaches(ctx, queryRepo, querySvc.Cache(), logger)

	mux := http.NewServeMux()
	loginLimiter := api.NewRateLimiter(cfg.LoginRatePerMinute, cfg.IngestRatePerMinute, cfg.APIRatePerMinute)
	loginRL := api.RateLimitMiddleware(loginLimiter, "login")
	mux.Handle("/api/v1/login", loginRL(api.HandleLogin(userAuth, sessionMgr, loginLimiter, func() {
		api.WarmLoginCaches(context.Background(), querySvc, logger)
	})))
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

	if cfg.GRPCAddr != "" {
		grpcSrv := api.NewGRPCServer(apiServer, logger)
		safeGo("grpc", &wg, func() {
			if err := grpcSrv.ListenAndServe(ctx, cfg.GRPCAddr); err != nil {
				logger.Error("grpc error", "error", err)
			}
		})
	}

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
	wg.Wait()
	db.FinalCheckpoint(checkpointMode(), logger)

	logger.Info("shutdown complete")
	return nil
}

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
		sessionMgr *auth.SessionManager
		userAuth   *auth.UserAuthenticator
		queryCache *cache.Cache
	)
	if cfg.DBPath != "" {
		db, dbErr := repository.NewReadOnlyDB(cfg.DBPath)
		if dbErr != nil {
			logger.Warn("read-only DB unavailable, dashboard reads disabled", "error", dbErr)
		} else {
			defer db.Close()
			roRepo = repository.NewReadOnlyRepository(db.DB)
			if cfg.QueryTimeoutSeconds > 0 {
				roRepo.SetQueryTimeout(time.Duration(cfg.QueryTimeoutSeconds) * time.Second)
			}
			keyLookup = &readOnlyKeyLookupAdapter{repo: roRepo}

			sessionMgr = auth.NewSessionManager(cfg.SessionSecret, int64(cfg.SessionTTLSeconds))
			userAuth = auth.NewUserAuthenticator(&userLookupAdapter{repo: roRepo}, logger)
			querySvc = service.NewQueryService(roRepo, logger, ingest.NewCachedRatioLookup(roRepo, time.Minute))

			ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
			var store cache.Store
			if cfg.RedisURL != "" {
				if rs, cacheErr := cache.NewRedisStore(cfg.RedisURL); cacheErr != nil {
					logger.Warn("redis cache unavailable, falling back to in-memory", "error", cacheErr)
					store = cache.NewMemoryStore()
				} else {
					store = rs
				}
			} else {
				store = cache.NewMemoryStore()
			}
			queryCache = cache.New(store, ttl)
			querySvc.SetCache(queryCache)
			defer queryCache.Close()

			logger.Info("read-only DB attached for dashboard", "path", cfg.DBPath)
		}
	}

	staticKeyHash := cfg.APIKeySHA256
	if staticKeyHash == "" && cfg.APIKey != "" {
		staticKeyHash = auth.HashKey(cfg.APIKey)
	}
	authorizer := auth.NewAuthorizer(staticKeyHash, keyLookup, logger)

	serverCfg := api.ServerConfig{
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
		FunnelBarnEndpoint: cfg.FunnelBarnEndpoint,
		FunnelBarnAPIKey:   cfg.FunnelBarnAPIKey,
		FunnelBarnProject:  cfg.FunnelBarnProject,
	}
	// Trace buffer: holds spans for up to 10 min then applies ratio-based
	// sampling per (project, operation). Error traces always pass intact.
	var ratioLookup ingest.SampleRatioLookup
	if roRepo != nil {
		ratioLookup = ingest.NewCachedRatioLookup(roRepo, time.Minute)
	}
	traceBuffer := ingest.NewTraceBuffer(ingest.DefaultTraceBufferTTL, ratioLookup)
	safeGo("trace-buffer-drain", &wg, func() {
		for {
			select {
			case <-readerCtx.Done():
				return
			case spans := <-traceBuffer.Out:
				for _, rec := range spans {
					ingestHandler.Enqueue(rec)
				}
			}
		}
	})

	opts := []api.ServerOption{api.WithAuthorizer(authorizer), api.WithTraceBuffer(traceBuffer), api.WithMetricsHandler(readerMetricsHandler), api.WithLogsHandler(readerLogsHandler)}
	if roRepo != nil {
		opts = append(opts, api.WithRepository(roRepo), api.WithPaths(cfg.DBPath, cfg.SpoolDir), api.WithCache(queryCache))
	}
	apiServer := api.NewServerWithQuery(serverCfg, ingestHandler, querySvc, sessionMgr, logger, opts...)
	if oidcClient := buildOIDCClient(cfg, logger); oidcClient != nil {
		apiServer.SetOIDCClient(oidcClient)
	}

	// Self-metrics for reader/ingest pod: request rates, latency, spool depth, queue depth.
	readerSelfRec := selfmetrics.NewRecorder()
	readerSelfRec.RegisterGauge("spanbarn.spool.bytes", map[string]string{"dir": cfg.SpoolDir}, func() float64 {
		return float64(eventSpool.Size())
	})
	for _, lbl := range []string{"spans", "metrics", "logs"} {
		lbl := lbl
		readerSelfRec.RegisterGauge("spanbarn.queue.depth", map[string]string{"queue": lbl}, func() float64 {
			depths := writeQueue.Depths(readerCtx)
			return float64(depths[lbl])
		})
	}
	apiServer.SetSelfMetricsRecorder(readerSelfRec)
	startSelfMetrics(readerCtx, cfg, &wg, readerSelfRec, logger)

	if cfg.GRPCAddr != "" {
		grpcSrv := api.NewGRPCServer(apiServer, logger)
		safeGo("grpc", &wg, func() {
			if err := grpcSrv.ListenAndServe(readerCtx, cfg.GRPCAddr); err != nil {
				logger.Error("grpc error", "error", err)
			}
		})
	}

	if roRepo != nil && queryCache != nil {
		api.WarmCaches(readerCtx, roRepo, queryCache, logger)
	}

	mux := http.NewServeMux()
	if sessionMgr != nil && userAuth != nil {
		loginLimiter := api.NewRateLimiter(cfg.LoginRatePerMinute, cfg.IngestRatePerMinute, cfg.APIRatePerMinute)
		loginRL := api.RateLimitMiddleware(loginLimiter, "login")
		mux.Handle("/api/v1/login", loginRL(api.HandleLogin(userAuth, sessionMgr, loginLimiter, func() {
			api.WarmLoginCaches(context.Background(), querySvc, logger)
		})))
		mux.Handle("/api/v1/logout", http.HandlerFunc(api.HandleLogout()))
	}
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
		logger.Info("reader listening", "addr", cfg.Addr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-readerCtx.Done():
		logger.Info("shutting down reader")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("reader shutdown error", "error", err)
	}

	ingestHandler.Stop()
	wg.Wait()
	logger.Info("reader shutdown complete")
	return nil
}

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
		slog.Warn("SPANBARN_SESSION_SECRET is not set; sessions will not persist across restarts")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Step 1: health endpoint up immediately so startup probes pass during migrations.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","mode":"writer"}`)
	})

	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	httpErrCh := make(chan error, 1)
	go func() {
		logger.Info("writer listening", "addr", cfg.Addr)
		httpErrCh <- httpServer.ListenAndServe()
	}()

	// Step 2: write DB — MaxOpenConns(1), used by worker/retention/aggregation/alerts.
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
	if cfg.QueryTimeoutSeconds > 0 {
		repo.SetQueryTimeout(time.Duration(cfg.QueryTimeoutSeconds) * time.Second)
	}

	if cfg.AdminUsername != "" && cfg.AdminPassword != "" {
		if err := bootstrapAdmin(repo, cfg, logger); err != nil {
			return err
		}
	}

	// Step 3: read-only DB for the query service — reads don't compete with writes.
	roDB, err := repository.NewReadOnlyDB(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open read-only database: %w", err)
	}
	defer roDB.Close()
	queryRepo := repository.NewRepository(roDB.DB)
	if cfg.QueryTimeoutSeconds > 0 {
		queryRepo.SetQueryTimeout(time.Duration(cfg.QueryTimeoutSeconds) * time.Second)
	}

	// Step 4: full API server wired into the already-listening mux.
	apiKeyHash := cfg.APIKeySHA256
	if apiKeyHash == "" && cfg.APIKey != "" {
		apiKeyHash = auth.HashKey(cfg.APIKey)
	}
	authorizer := auth.NewAuthorizer(apiKeyHash, &keyLookupAdapter{repo: repo}, logger)
	userAuth := auth.NewUserAuthenticator(&userLookupAdapter{repo: repo}, logger)
	sessionMgr := auth.NewSessionManager(cfg.SessionSecret, int64(cfg.SessionTTLSeconds))

	writerRatioLookup := ingest.NewCachedRatioLookup(queryRepo, time.Minute)
	querySvc := service.NewQueryService(queryRepo, logger, writerRatioLookup)
	{
		ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
		var store cache.Store
		if cfg.RedisURL != "" {
			rs, cacheErr := cache.NewRedisStore(cfg.RedisURL)
			if cacheErr != nil {
				logger.Warn("redis cache unavailable, falling back to in-memory", "error", cacheErr)
				store = cache.NewMemoryStore()
			} else {
				store = rs
			}
		} else {
			store = cache.NewMemoryStore()
		}
		queryCache := cache.New(store, ttl)
		querySvc.SetCache(queryCache)
		defer queryCache.Close()
	}

	serverCfg := api.ServerConfig{
		APIKey:             cfg.APIKey,
		MaxBodyBytes:       cfg.MaxBodyBytes,
		AllowedOrigins:     cfg.AllowedOrigins,
		Version:            Version,
		Environment:        cfg.Environment,
		MetricsToken:       cfg.MetricsToken,
		LoginRate:          cfg.LoginRatePerMinute,
		IngestRate:         cfg.IngestRatePerMinute,
		APIRate:            cfg.APIRatePerMinute,
		SessionSecret:      cfg.SessionSecret,
		PublicURL:          cfg.PublicURL,
		FunnelBarnEndpoint: cfg.FunnelBarnEndpoint,
		FunnelBarnAPIKey:   cfg.FunnelBarnAPIKey,
		FunnelBarnProject:  cfg.FunnelBarnProject,
	}
	// No ingest handler — OTLP goes to the reader pod per ingress rules.
	apiServer := api.NewServerWithQuery(serverCfg, nil, querySvc, sessionMgr, logger,
		api.WithRepository(repo),
		api.WithAuthorizer(authorizer),
		api.WithPaths(cfg.DBPath, cfg.SpoolDir),
		api.WithCache(querySvc.Cache()),
	)
	if oidcClient := buildOIDCClient(cfg, logger); oidcClient != nil {
		apiServer.SetOIDCClient(oidcClient)
	}
	loginLimiter := api.NewRateLimiter(cfg.LoginRatePerMinute, cfg.IngestRatePerMinute, cfg.APIRatePerMinute)
	loginRL := api.RateLimitMiddleware(loginLimiter, "login")
	mux.Handle("/api/v1/login", loginRL(api.HandleLogin(userAuth, sessionMgr, loginLimiter, func() {
		api.WarmLoginCaches(context.Background(), querySvc, logger)
	})))
	mux.Handle("/api/v1/logout", http.HandlerFunc(api.HandleLogout()))
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
	for _, lbl := range []string{"spans", "metrics", "logs"} {
		lbl := lbl
		writerSelfRec.RegisterGauge("spanbarn.queue.depth", map[string]string{"queue": lbl}, func() float64 {
			depths := writeQueue.Depths(ctx)
			return float64(depths[lbl])
		})
	}
	startSelfMetrics(ctx, cfg, &wg, writerSelfRec, logger)

	scheduler := writescheduler.New()
	repo.SetWriteScheduler(scheduler)

	boringPolicy := worker.NewCachedBoringPolicy(repo, 30*time.Second)

	rw := worker.NewRedisWorker(writeQueue, &workerRepoAdapter{repo: repo}, logger)
	rw.SetAccumulator(accumulator)
	rw.SetConfig(worker.WorkerConfig{SlowThresholdUs: int64(cfg.SlowThresholdMS) * 1000})
	rw.SetBoringPolicy(boringPolicy)
	// The writer is the single SQLite writer, so an in-memory per-minute floor
	// counts boring-trace survivals accurately across batches.
	rw.SetMinuteFloor(sampling.NewMinuteFloor())
	workerCtx, workerCancel := context.WithCancel(ctx)
	safeGo("write-scheduler", &wg, func() { scheduler.Run(workerCtx) })
	defer workerCancel()
	safeGo("redis-worker", &wg, func() { rw.Run(workerCtx) })
	safeGo("accumulator", &wg, func() { accumulator.Run(workerCtx) })
	safeGo("metric-accumulator", &wg, func() { metricAccumulator.Run(workerCtx) })
	safeGo("metrics-consumer", &wg, func() {
		for {
			select {
			case <-workerCtx.Done():
				return
			default:
			}
			recs, err := writeQueue.ConsumeMetrics(workerCtx)
			if err != nil {
				logger.Error("metrics consumer error", "error", err)
				continue
			}
			if len(recs) == 0 {
				continue
			}
			for i := range recs {
				metricAccumulator.AddMetric(recs[i])
			}
			if err := repo.InsertMetrics(workerCtx, recs); err != nil {
				logger.Error("metrics insert error", "error", err)
			}
		}
	})
	safeGo("logs-consumer", &wg, func() {
		for {
			select {
			case <-workerCtx.Done():
				return
			default:
			}
			recs, err := writeQueue.ConsumeLogs(workerCtx)
			if err != nil {
				logger.Error("logs consumer error", "error", err)
				continue
			}
			if len(recs) == 0 {
				continue
			}
			if err := repo.InsertLogs(workerCtx, recs); err != nil {
				logger.Error("logs insert error", "error", err)
			}
		}
	})
	cpMode := checkpointMode()
	// Under Litestream (PASSIVE) the WAL grows under sustained read load; escalate
	// to a bounding TRUNCATE only once it exceeds the configured size (~256 WAL
	// frames per MiB at the default 4 KiB page size). 0 = never (standalone TRUNCATE).
	cpTruncateFrames := 0
	if cpMode == repository.CheckpointPassive {
		cpTruncateFrames = cfg.WALTruncateThresholdMB * 256
	}
	// While the span write-queue is backed up, skip checkpoints so the single
	// writer connection isn't blocked (up to busy_timeout) draining the backlog.
	cpBusy := func() bool {
		if cfg.CheckpointSkipQueueDepth <= 0 {
			return false
		}
		n, err := writeQueue.Len(workerCtx)
		return err == nil && n > int64(cfg.CheckpointSkipQueueDepth)
	}
	safeGo("wal-checkpoint", &wg, func() { db.RunPeriodicCheckpoint(workerCtx, 30*time.Second, cpMode, cpTruncateFrames, cpBusy, logger) })
	if litestreamActive() {
		logger.Info("litestream active: writer runs PASSIVE WAL checkpoints; Litestream owns WAL truncation")
	}

	// Retention queries (DELETE/SELECT on the full spans table) can take
	// several minutes when the backlog is large. Give it a separate repo
	// instance with a longer timeout so individual queries don't abort
	// mid-cycle. Both repos share the same *sql.DB connection and write
	// scheduler, so all writes are correctly serialised and prioritised.
	retentionRepo := repository.NewRepository(db.DB)
	retentionRepo.SetQueryTimeout(5 * time.Minute)
	retentionRepo.SetWriteScheduler(scheduler)
	retentionRepo.SetDeleteBatchYield(time.Duration(cfg.RetentionDeleteBatchYieldMS) * time.Millisecond)

	retentionCfg := retention.Config{
		FullRetentionHours:        cfg.RetentionFullHours,
		InterestingRetentionHours: cfg.RetentionInterestingHours,
		BoringRetentionMinutes:    cfg.BoringRetentionMinutes,
		ErrorRetentionDays:        cfg.RetentionErrorDays,
		AggregateRetentionDays:    cfg.RetentionAggregatedDays,
		MetricsRetentionDays:      cfg.MetricsRetentionDays,
		LogRetentionHours:         cfg.LogRetentionHours,
		ErrorLogRetentionDays:     cfg.ErrorLogRetentionDays,
		SlowThresholdUS:           int64(cfg.SlowThresholdMS) * 1000,
	}
	retentionWorker := retention.NewRetentionWorker(retentionRepo, accumulator, retentionCfg, logger)
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	defer retentionCancel()
	safeGo("retention", &wg, func() { retentionWorker.Run(retentionCtx) })

	alertNotifier := alert.NewDefaultNotifier(alert.NotifierConfig{}, logger)
	// Alert reads run on the read-only connection; the rare trigger write stays
	// on the writable repo. Keeps alert evaluation off the single writer path.
	alertEval := alert.NewEvaluator(queryRepo, alertNotifier, logger, writerRatioLookup)
	alertEval.SetTriggerWriter(repo)
	alertRunner := alert.NewRunner(alertEval, queryRepo, time.Minute, logger)
	alertCtx, alertCancel := context.WithCancel(ctx)
	defer alertCancel()
	safeGo("alert-runner", &wg, func() { alertRunner.Run(alertCtx) })

	querySvc.SetAccumulator(accumulator)
	api.WarmCaches(ctx, queryRepo, querySvc.Cache(), logger)

	select {
	case <-ctx.Done():
		logger.Info("shutting down writer")
	case err := <-httpErrCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("writer shutdown error", "error", err)
	}

	alertCancel()
	retentionCancel()
	workerCancel()
	wg.Wait()
	db.FinalCheckpoint(checkpointMode(), logger)

	logger.Info("writer shutdown complete")
	return nil
}

// buildOIDCClient returns an OIDC adapter when all four SPANBARN_OIDC_* vars
// are set, or nil otherwise (in which case the local single-user login is the
// only auth path). Discovery is lazy so an unreachable issuer at startup does
// not crash the process.
func buildOIDCClient(cfg config.Config, logger *slog.Logger) *auth.OIDCClient {
	oc := auth.OIDCConfig{
		Issuer:            cfg.OIDCIssuer,
		ClientID:          cfg.OIDCClientID,
		ClientSecret:      cfg.OIDCClientSecret,
		RedirectURL:       cfg.OIDCRedirectURL,
		RequiredGroup:     cfg.OIDCRequiredGroup,
		ResourceAudiences: cfg.OIDCResourceAudiences,
		CLIClientID:       cfg.OIDCCLIClientID,
	}
	// The sb CLI's device-code tokens carry the CLI client id as their
	// audience, so accept it as a resource audience automatically.
	if cfg.OIDCCLIClientID != "" {
		oc.ResourceAudiences = append(oc.ResourceAudiences, cfg.OIDCCLIClientID)
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panic",
					"goroutine", name,
					"panic", fmt.Sprint(r),
					"stack", string(debug.Stack()),
				)
			}
		}()
		fn()
	}()
}

// litestreamActive returns true when this process is running as the -exec child
// of Litestream. In that case Litestream owns WAL generations and truncation as
// part of its replication cycle, so the writer must checkpoint in PASSIVE mode
// (see checkpointMode) — a TRUNCATE would reset the WAL header and force
// Litestream into a costly full re-snapshot.
func litestreamActive() bool {
	return os.Getenv("LITESTREAM_ACCESS_KEY_ID") != ""
}

// checkpointMode picks the WAL checkpoint strategy for this process: PASSIVE when
// a Litestream replica is attached (so we never reset its WAL generation), else
// TRUNCATE for aggressive standalone WAL bounding.
func checkpointMode() repository.CheckpointMode {
	if litestreamActive() {
		return repository.CheckpointPassive
	}
	return repository.CheckpointTruncate
}

// startSelfMetrics wires and launches the periodic self-metrics reporter, which
// POSTs SpanBarn's own OTLP metrics to its ingest endpoint so the Metrics page
// always has live data (dogfooding). A nil rec or disabled config is a no-op.
func startSelfMetrics(ctx context.Context, cfg config.Config, wg *sync.WaitGroup, rec *selfmetrics.Recorder, logger *slog.Logger) {
	if rec == nil || cfg.SelfMetricsDisabled {
		return
	}
	endpoint := cfg.SelfEndpoint
	apiKey := cfg.SelfAPIKey
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
		time.Duration(cfg.SelfMetricsIntervalSec)*time.Second,
		map[string]string{
			"service.name":     "spanbarn",
			"spanbarn.mode":    cfg.Mode,
			"spanbarn.version": Version,
		},
		startNano,
		logger,
	)
	logger.Info("self-metrics enabled", "endpoint", endpoint, "interval_s", cfg.SelfMetricsIntervalSec)
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

func (a *workerRepoAdapter) InsertPromptRecords(_ context.Context, records []repository.PromptRecord) error {
	return a.repo.InsertPromptRecords(records)
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

// readOnlyKeyLookupAdapter is used by the ingest pod which opens the database
// in read-only mode. TouchAPIKey is a no-op because last_used_at can be
// inferred from the presence of spans in the writer's database.
type readOnlyKeyLookupAdapter struct {
	repo *repository.Repository
}

func (a *readOnlyKeyLookupAdapter) GetAPIKeyByHash(keyHash string) (auth.APIKeyRecord, error) {
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

func (a *readOnlyKeyLookupAdapter) TouchAPIKey(_ int64) error { return nil }

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
		db, dbErr := repository.NewReadOnlyDB(cfg.DBPath)
		if dbErr != nil {
			logger.Warn("read-only DB unavailable, API key validation limited to static key", "error", dbErr)
		} else {
			defer db.Close()
			roRepo = repository.NewReadOnlyRepository(db.DB)
			if cfg.QueryTimeoutSeconds > 0 {
				roRepo.SetQueryTimeout(time.Duration(cfg.QueryTimeoutSeconds) * time.Second)
			}
			keyLookup = &readOnlyKeyLookupAdapter{repo: roRepo}
			logger.Info("read-only DB attached for failover reads", "path", cfg.DBPath)
		}
	}
	staticKeyHash := cfg.APIKeySHA256
	if staticKeyHash == "" && cfg.APIKey != "" {
		staticKeyHash = auth.HashKey(cfg.APIKey)
	}
	authorizer := auth.NewAuthorizer(staticKeyHash, keyLookup, logger)

	var (
		querySvc   *service.QueryService
		sessionMgr *auth.SessionManager
		userAuth   *auth.UserAuthenticator
		queryCache *cache.Cache
	)
	if roRepo != nil {
		sessionMgr = auth.NewSessionManager(cfg.SessionSecret, int64(cfg.SessionTTLSeconds))
		userAuth = auth.NewUserAuthenticator(&userLookupAdapter{repo: roRepo}, logger)
		querySvc = service.NewQueryService(roRepo, logger, ingest.NewCachedRatioLookup(roRepo, time.Minute))
		ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
		var store cache.Store
		if cfg.RedisURL != "" {
			if rs, cacheErr := cache.NewRedisStore(cfg.RedisURL); cacheErr != nil {
				logger.Warn("redis unavailable, falling back to in-memory cache", "error", cacheErr)
				store = cache.NewMemoryStore()
			} else {
				store = rs
			}
		} else {
			store = cache.NewMemoryStore()
		}
		queryCache = cache.New(store, ttl)
		querySvc.SetCache(queryCache)
		defer queryCache.Close()
	}

	serverCfg := api.ServerConfig{
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
		FunnelBarnEndpoint: cfg.FunnelBarnEndpoint,
		FunnelBarnAPIKey:   cfg.FunnelBarnAPIKey,
		FunnelBarnProject:  cfg.FunnelBarnProject,
	}
	opts := []api.ServerOption{api.WithAuthorizer(authorizer)}
	if roRepo != nil {
		opts = append(opts, api.WithRepository(roRepo), api.WithPaths(cfg.DBPath, cfg.SpoolDir), api.WithCache(queryCache))
	}
	apiServer := api.NewServerWithQuery(serverCfg, ingestHandler, querySvc, sessionMgr, logger, opts...)
	if oidcClient := buildOIDCClient(cfg, logger); oidcClient != nil {
		apiServer.SetOIDCClient(oidcClient)
	}

	if roRepo != nil && queryCache != nil {
		api.WarmCaches(ctx, roRepo, queryCache, logger)
	}

	mux := http.NewServeMux()
	if sessionMgr != nil && userAuth != nil {
		loginLimiter := api.NewRateLimiter(cfg.LoginRatePerMinute, cfg.IngestRatePerMinute, cfg.APIRatePerMinute)
		loginRL := api.RateLimitMiddleware(loginLimiter, "login")
		mux.Handle("/api/v1/login", loginRL(api.HandleLogin(userAuth, sessionMgr, loginLimiter, func() {
			api.WarmLoginCaches(context.Background(), querySvc, logger)
		})))
		mux.Handle("/api/v1/logout", http.HandlerFunc(api.HandleLogout()))
	}
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
		logger.Info("ingest listening", "addr", cfg.Addr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down ingest")
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

	ingestHandler.Stop()
	wg.Wait()
	logger.Info("ingest shutdown complete")
	return nil
}
