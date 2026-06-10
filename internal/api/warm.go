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

// WarmLoginCaches pre-populates the per-project Services cache for all
// dashboard time ranges so the first page load after login hits Redis rather
// than SQLite. Runs entirely in the background; login latency is unaffected.
func WarmLoginCaches(ctx context.Context, repo *repository.Repository, qs *service.QueryService, logger *slog.Logger) {
	if repo == nil || qs == nil {
		return
	}
	go func() {
		bctx, rootSpan := apiTracer.Start(context.Background(), "api.warm.login")
		defer rootSpan.End()

		projects, err := repo.ListProjects()
		if err != nil {
			rootSpan.RecordError(err)
			rootSpan.SetStatus(codes.Error, "list projects failed")
			logger.Warn("warm login: list projects failed", "err", err)
			return
		}
		rootSpan.SetAttributes(attribute.Int("projects", len(projects)))

		ranges := []time.Duration{time.Hour, 4 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour}
		now := time.Now()
		errors := 0
		for _, p := range projects {
			_, pSpan := apiTracer.Start(bctx, "api.warm.login.project")
			pSpan.SetAttributes(attribute.Int64("project.id", p.ID))
			for _, d := range ranges {
				_, err := qs.ListServices(bctx, p.ID, now.Add(-d), now, false)
				if err != nil {
					pSpan.RecordError(fmt.Errorf("range %s: %w", d, err))
					logger.Warn("warm login: list services failed", "project", p.ID, "range", d, "err", err)
					errors++
				}
			}
			pSpan.End()
		}

		rootSpan.SetAttributes(
			attribute.Int("ranges", len(ranges)),
			attribute.Int("errors", errors),
		)
		if errors > 0 {
			rootSpan.SetStatus(codes.Error, "some ranges failed")
		}
		logger.Info("warm login: complete", "projects", len(projects), "errors", errors)
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
