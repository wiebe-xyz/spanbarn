package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
)

// newHTTPServer builds the http.Server every serving mode uses.
func newHTTPServer(cfg config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:         cfg.Addr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

// listenAsync starts httpServer in the background and returns the channel that
// receives its terminal error.
func listenAsync(httpServer *http.Server, logger *slog.Logger, msg string) chan error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info(msg, "addr", httpServer.Addr)
		errCh <- httpServer.ListenAndServe()
	}()
	return errCh
}

// waitForShutdown blocks until ctx is cancelled (done=false, graceful path) or
// the server stops on its own (done=true, with err unless it was a clean close).
func waitForShutdown(ctx context.Context, errCh <-chan error, logger *slog.Logger, msg string) (done bool, err error) {
	select {
	case <-ctx.Done():
		logger.Info(msg)
		return false, nil
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return true, err
		}
		return true, nil
	}
}

// shutdownHTTP gracefully stops httpServer within 10 seconds.
func shutdownHTTP(httpServer *http.Server, logger *slog.Logger, errMsg string) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error(errMsg, "error", err)
	}
}

// startGRPC serves the gRPC ingest endpoint when cfg.GRPCAddr is set.
func startGRPC(ctx context.Context, cfg config.Config, wg *sync.WaitGroup, apiServer *api.Server, logger *slog.Logger) {
	if cfg.GRPCAddr == "" {
		return
	}
	grpcSrv := api.NewGRPCServer(apiServer, logger)
	safeGo("grpc", wg, func() {
		if err := grpcSrv.ListenAndServe(ctx, cfg.GRPCAddr); err != nil {
			logger.Error("grpc error", "error", err)
		}
	})
}

// drainTraceBuffer forwards sampled traces from the buffer into the ingest
// handler until ctx is cancelled.
func drainTraceBuffer(ctx context.Context, wg *sync.WaitGroup, buf *ingest.TraceBuffer, handler *ingest.Handler) {
	safeGo("trace-buffer-drain", wg, func() {
		for {
			select {
			case <-ctx.Done():
				return
			case spans := <-buf.Out:
				for _, rec := range spans {
					handler.Enqueue(rec)
				}
			}
		}
	})
}

// newQueryCache builds the dashboard query cache backed by Redis when
// configured and reachable, otherwise in memory. verbose adds the success logs
// standalone mode emits.
func newQueryCache(cfg config.Config, logger *slog.Logger, verbose bool) *cache.Cache {
	ttl := time.Duration(cfg.CacheTTLSeconds) * time.Second
	var store cache.Store
	if cfg.RedisURL != "" {
		rs, cacheErr := cache.NewRedisStore(cfg.RedisURL)
		if cacheErr != nil {
			logger.Info("redis cache unavailable, using in-memory cache", "error", cacheErr)
			store = cache.NewMemoryStore()
		} else {
			store = rs
			if verbose {
				logger.Info("redis cache enabled", "ttl", ttl)
			}
		}
	} else {
		store = cache.NewMemoryStore()
		if verbose {
			logger.Info("in-memory cache enabled", "ttl", ttl)
		}
	}
	return cache.New(store, ttl)
}

// staticKeyHash returns the SHA-256 of the configured static API key.
func staticKeyHash(cfg config.Config) string {
	if cfg.APIKeySHA256 == "" && cfg.APIKey != "" {
		return auth.HashKey(cfg.APIKey)
	}
	return cfg.APIKeySHA256
}

// applyQueryTimeout sets the configured per-query timeout on repo.
func applyQueryTimeout(repo *repository.Repository, cfg config.Config) {
	if cfg.QueryTimeoutSeconds > 0 {
		repo.SetQueryTimeout(time.Duration(cfg.QueryTimeoutSeconds) * time.Second)
	}
}

// openWriteRepo opens the single-connection write DB, runs migrations and
// returns the repository on top of it.
func openWriteRepo(cfg config.Config, logger *slog.Logger) (*repository.DB, *repository.Repository, error) {
	db, err := repository.NewDBWithCache(cfg.DBPath, cfg.SQLiteCacheMB, cfg.SQLiteMmapMB)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	if err := repository.Migrate(db.DB); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("run migrations: %w", err)
	}
	logger.Info("storage", "path", cfg.DBPath)
	repo := repository.NewRepository(db.DB)
	applyQueryTimeout(repo, cfg)
	return db, repo, nil
}

// openQueryRepo opens the read-only DB used for dashboard reads.
func openQueryRepo(cfg config.Config) (*repository.DB, *repository.Repository, error) {
	roDB, err := repository.NewReadOnlyDBWithCache(cfg.DBPath, cfg.SQLiteROCacheMB, cfg.SQLiteROMmapMB)
	if err != nil {
		return nil, nil, fmt.Errorf("open read-only database: %w", err)
	}
	queryRepo := repository.NewRepository(roDB.DB)
	applyQueryTimeout(queryRepo, cfg)
	return roDB, queryRepo, nil
}

// registerQueueDepthGauges exposes the write queue depths as self-metrics.
func registerQueueDepthGauges(ctx context.Context, rec *selfmetrics.Recorder, q *queue.RedisQueue) {
	for _, lbl := range []string{"spans", "metrics", "logs"} {
		lbl := lbl
		rec.RegisterGauge("spanbarn.queue.depth", map[string]string{"queue": lbl}, func() float64 {
			return float64(q.Depths(ctx)[lbl])
		})
	}
}
