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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ingestHandler.Start(ctx)

	var wg sync.WaitGroup

	aggInterval := parseAggregationInterval(cfg.AggregationInterval)
	aggregator := aggregation.NewAggregator(repo, aggInterval, logger)

	w := worker.NewWorker(eventSpool, &workerRepoAdapter{repo: repo}, logger)
	w.SetAggregator(aggregator)
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	safeGo("worker", &wg, func() { w.Run(workerCtx) })
	// Always run the app-side checkpoint even when Litestream is active.
	// Litestream's own PASSIVE checkpoint has no busy_timeout on its connection
	// and returns SQLITE_BUSY immediately when a write transaction is in flight
	// (~20 span writes/s means it almost never succeeds). The app's TRUNCATE
	// checkpoint has busy_timeout(30000) and will wait for a clear window.
	// WAL-level reader locks ensure Litestream's unstreamed frames are never
	// truncated before S3 confirms them, so dual checkpointing is safe.
	safeGo("wal-checkpoint", &wg, func() { db.RunPeriodicCheckpoint(workerCtx, 30*time.Second, logger) })
	if litestreamActive() {
		logger.Info("litestream active: WAL checkpoint also running in-process with busy_timeout")
	}

	retentionCfg := retention.Config{
		FullRetentionHours:        cfg.RetentionFullHours,
		InterestingRetentionHours: cfg.RetentionInterestingHours,
		BoringRetentionMinutes:    cfg.BoringRetentionMinutes,
		ErrorRetentionDays:        cfg.RetentionErrorDays,
		AggregateRetentionDays:    cfg.RetentionAggregatedDays,
		SlowThresholdUS:           int64(cfg.SlowThresholdMS) * 1000,
	}
	retentionWorker := retention.NewRetentionWorker(repo, aggregator, retentionCfg, logger)
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	defer retentionCancel()
	safeGo("retention", &wg, func() { retentionWorker.Run(retentionCtx) })

	ratioLookup := ingest.NewCachedRatioLookup(queryRepo, time.Minute)

	alertNotifier := alert.NewDefaultNotifier(alert.NotifierConfig{}, logger)
	alertEval := alert.NewEvaluator(repo, alertNotifier, logger, ratioLookup)
	alertRunner := alert.NewRunner(alertEval, repo, time.Minute, logger)
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
	)
	if oidcClient := buildOIDCClient(cfg, logger); oidcClient != nil {
		apiServer.SetOIDCClient(oidcClient)
	}

	api.WarmCaches(ctx, queryRepo, querySvc.Cache(), logger)

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
	wg.Wait()
	db.FinalCheckpoint(logger)

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

	var wg sync.WaitGroup
	fwd := forward.NewRedisForwarder(eventSpool, writeQueue, logger)
	safeGo("redis-forwarder", &wg, func() { fwd.Run(readerCtx) })


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

	opts := []api.ServerOption{api.WithAuthorizer(authorizer), api.WithTraceBuffer(traceBuffer)}
	if roRepo != nil {
		opts = append(opts, api.WithRepository(roRepo), api.WithPaths(cfg.DBPath, cfg.SpoolDir), api.WithCache(queryCache))
	}
	apiServer := api.NewServerWithQuery(serverCfg, ingestHandler, querySvc, sessionMgr, logger, opts...)
	if oidcClient := buildOIDCClient(cfg, logger); oidcClient != nil {
		apiServer.SetOIDCClient(oidcClient)
	}

	if roRepo != nil && queryCache != nil {
		api.WarmCaches(readerCtx, roRepo, queryCache, logger)
	}

	mux := http.NewServeMux()
	if sessionMgr != nil && userAuth != nil {
		loginRL := api.RateLimitMiddleware(
			api.NewRateLimiter(cfg.LoginRatePerMinute, cfg.IngestRatePerMinute, cfg.APIRatePerMinute),
			"login",
		)
		mux.Handle("/api/v1/login", loginRL(api.HandleLogin(userAuth, sessionMgr)))
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
	loginRL := api.RateLimitMiddleware(api.NewRateLimiter(cfg.LoginRatePerMinute, cfg.IngestRatePerMinute, cfg.APIRatePerMinute), "login")
	mux.Handle("/api/v1/login", loginRL(api.HandleLogin(userAuth, sessionMgr)))
	mux.Handle("/api/v1/logout", http.HandlerFunc(api.HandleLogout()))
	mux.Handle("/", apiServer.Handler())
	logger.Info("writer API ready")

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

	// writeMu serialises all SQLite writes between the span worker and the
	// retention worker. Without this they compete for the write connection:
	// retention times out waiting for the lock, and retries compound the
	// contention. With the mutex each side waits at the Go level (cheap) and
	// SQLite only ever sees one writer at a time.
	writeMu := &sync.Mutex{}

	boringPolicy := worker.NewCachedBoringPolicy(repo, 30*time.Second)

	rw := worker.NewRedisWorker(writeQueue, &workerRepoAdapter{repo: repo}, logger)
	rw.SetAccumulator(accumulator)
	rw.SetConfig(worker.WorkerConfig{SlowThresholdUs: int64(cfg.SlowThresholdMS) * 1000})
	rw.SetBoringPolicy(boringPolicy)
	rw.SetWriteMutex(writeMu)
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	safeGo("redis-worker", &wg, func() { rw.Run(workerCtx) })
	safeGo("accumulator", &wg, func() { accumulator.Run(workerCtx) })
	safeGo("wal-checkpoint", &wg, func() { db.RunPeriodicCheckpoint(workerCtx, 30*time.Second, logger) })
	if litestreamActive() {
		logger.Info("litestream active: WAL checkpoint also running in-process with busy_timeout")
	}

	// Retention queries (DELETE/SELECT on the full spans table) can take
	// several minutes when the backlog is large. Give it a separate repo
	// instance with a longer timeout so individual queries don't abort
	// mid-cycle. Both repos share the same *sql.DB connection, so the
	// writeMu still serialises all writes correctly.
	retentionRepo := repository.NewRepository(db.DB)
	retentionRepo.SetQueryTimeout(5 * time.Minute)

	retentionCfg := retention.Config{
		FullRetentionHours:        cfg.RetentionFullHours,
		InterestingRetentionHours: cfg.RetentionInterestingHours,
		BoringRetentionMinutes:    cfg.BoringRetentionMinutes,
		ErrorRetentionDays:        cfg.RetentionErrorDays,
		AggregateRetentionDays:    cfg.RetentionAggregatedDays,
		SlowThresholdUS:           int64(cfg.SlowThresholdMS) * 1000,
	}
	retentionWorker := retention.NewRetentionWorker(retentionRepo, accumulator, retentionCfg, logger)
	retentionWorker.SetWriteMutex(writeMu)
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	defer retentionCancel()
	safeGo("retention", &wg, func() { retentionWorker.Run(retentionCtx) })

	alertNotifier := alert.NewDefaultNotifier(alert.NotifierConfig{}, logger)
	alertEval := alert.NewEvaluator(repo, alertNotifier, logger, writerRatioLookup)
	alertRunner := alert.NewRunner(alertEval, repo, time.Minute, logger)
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
	db.FinalCheckpoint(logger)

	logger.Info("writer shutdown complete")
	return nil
}

// buildOIDCClient returns an OIDC adapter when all four SPANBARN_OIDC_* vars
// are set, or nil otherwise (in which case the local single-user login is the
// only auth path). Discovery is lazy so an unreachable issuer at startup does
// not crash the process.
func buildOIDCClient(cfg config.Config, logger *slog.Logger) *auth.OIDCClient {
	oc := auth.OIDCConfig{
		Issuer:        cfg.OIDCIssuer,
		ClientID:      cfg.OIDCClientID,
		ClientSecret:  cfg.OIDCClientSecret,
		RedirectURL:   cfg.OIDCRedirectURL,
		RequiredGroup: cfg.OIDCRequiredGroup,
	}
	if !oc.Enabled() {
		return nil
	}
	logger.Info("oidc: enabled", "issuer", oc.Issuer, "client_id", oc.ClientID, "required_group", oc.RequiredGroup)
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
// of Litestream. In that case Litestream owns WAL checkpointing as part of its
// replication cycle; spanbarn must not run a competing checkpoint goroutine.
func litestreamActive() bool {
	return os.Getenv("LITESTREAM_ACCESS_KEY_ID") != ""
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
		roRepo   *repository.Repository
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
		loginRL := api.RateLimitMiddleware(
			api.NewRateLimiter(cfg.LoginRatePerMinute, cfg.IngestRatePerMinute, cfg.APIRatePerMinute),
			"login",
		)
		mux.Handle("/api/v1/login", loginRL(api.HandleLogin(userAuth, sessionMgr)))
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
