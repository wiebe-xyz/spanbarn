package api

import (
	"context"
	"log/slog"
	"time"

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

// WarmLoginCaches pre-populates the per-project Services cache for the 1h and
// 24h ranges so the first dashboard page load after login hits Redis rather than
// SQLite. Runs entirely in the background; login latency is unaffected.
func WarmLoginCaches(ctx context.Context, repo *repository.Repository, qs *service.QueryService, logger *slog.Logger) {
	if repo == nil || qs == nil {
		return
	}
	go func() {
		projects, err := repo.ListProjects()
		if err != nil {
			logger.Warn("warm login: list projects failed", "err", err)
			return
		}
		now := time.Now()
		for _, p := range projects {
			for _, d := range []time.Duration{time.Hour, 4 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour} {
				if _, err := qs.ListServices(ctx, p.ID, now.Add(-d), now, false); err != nil {
					logger.Warn("warm login: list services failed", "project", p.ID, "err", err)
				}
			}
		}
		logger.Info("warm login: complete", "projects", len(projects))
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
