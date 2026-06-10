package api

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// WarmCaches populates the Redis SWR entries for the expensive read endpoints
// (projects/stats and stats/counts) so the first user-facing request is never
// blocked by cold compute. Without this, a cold cache + 30s HTTP WriteTimeout
// turns into 502s for the user. Idempotent and safe to call repeatedly.
//
// Each warmer runs in its own goroutine and writes directly to the cache —
// the HTTP handlers will see populated entries on first hit.
func WarmCaches(ctx context.Context, repo *repository.Repository, c *cache.Cache, logger *slog.Logger) {
	if repo == nil || c == nil {
		return
	}

	go warmEntry(ctx, c, logger, "projects:stats:24h", projectsStatsFresh, projectsStatsStale,
		func(ctx context.Context) (any, error) {
			s, err := repo.ProjectUsageStatsAll(24)
			if err != nil {
				return nil, err
			}
			if s == nil {
				return []repository.ProjectUsageStats{}, nil
			}
			return s, nil
		})

	go warmEntry(ctx, c, logger, "stats:counts", statsCountsFresh, statsCountsStale,
		func(ctx context.Context) (any, error) {
			return repo.GetDBCounts()
		})
}

// WarmLoginCaches pre-populates the Services and Prompts caches for all
// dashboard time ranges so the first page load after login hits Redis rather
// than SQLite. Runs entirely in the background; login latency is unaffected.
//
// The services endpoint is queried with project_id=0 (all-projects view) and
// both serverOnly variants to match the two states of the entry-points toggle.
func WarmLoginCaches(ctx context.Context, qs *service.QueryService, logger *slog.Logger) {
	if qs == nil {
		return
	}
	go func() {
		_, rootSpan := apiTracer.Start(context.Background(), "api.warm.login")
		defer rootSpan.End()

		ranges := []time.Duration{time.Hour, 4 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour}
		now := time.Now()
		errors := 0

		// Services: both serverOnly variants (default=true, toggled=false).
		for _, serverOnly := range []bool{true, false} {
			_, sSpan := apiTracer.Start(context.Background(), "api.warm.login.services")
			sSpan.SetAttributes(attribute.Bool("server_only", serverOnly))
			for _, d := range ranges {
				_, err := qs.ListServices(context.Background(), 0, now.Add(-d), now, serverOnly)
				if err != nil {
					sSpan.RecordError(fmt.Errorf("range %s: %w", d, err))
					logger.Warn("warm login: list services failed", "server_only", serverOnly, "range", d, "err", err)
					errors++
				}
			}
			sSpan.End()
		}

		// Prompts: no service/model filter (the default landing state).
		_, pSpan := apiTracer.Start(context.Background(), "api.warm.login.prompts")
		for _, d := range ranges {
			_, err := qs.ListPrompts(context.Background(), 0, now.Add(-d), now, "", "")
			if err != nil {
				pSpan.RecordError(fmt.Errorf("range %s: %w", d, err))
				logger.Warn("warm login: list prompts failed", "range", d, "err", err)
				errors++
			}
		}
		pSpan.End()

		rootSpan.SetAttributes(
			attribute.Int("ranges", len(ranges)),
			attribute.Int("errors", errors),
		)
		if errors > 0 {
			rootSpan.SetStatus(codes.Error, "some queries failed")
		}
		logger.Info("warm login: complete", "ranges", len(ranges), "errors", errors)
	}()
}

func warmEntry(
	ctx context.Context,
	c *cache.Cache,
	logger *slog.Logger,
	key string,
	freshTTL, staleTTL time.Duration,
	compute func(ctx context.Context) (any, error),
) {
	if _, already := statsRevalidating.LoadOrStore(key, true); already {
		return
	}
	defer statsRevalidating.Delete(key)

	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	start := time.Now()
	v, err := compute(cctx)
	if err != nil {
		logger.Warn("warmer: compute failed", "key", key, "elapsed", time.Since(start), "error", err)
		return
	}
	cache.SetSWR(c, cctx, key, v, freshTTL, staleTTL)
	logger.Info("warmer: cache populated", "key", key, "elapsed", time.Since(start))
}
