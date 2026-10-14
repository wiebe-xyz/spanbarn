package api

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/observability"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// WarmCaches populates the Redis SWR entries for the expensive read endpoints
// (projects/stats and stats/counts) so the first user-facing request is never
// blocked by cold compute. Without this, a cold cache + 30s HTTP WriteTimeout
// turns into 502s for the user. Idempotent and safe to call repeatedly.
//
// Each warmer runs in its own goroutine and writes directly to the cache —
// the HTTP handlers will see populated entries on first hit.
func WarmCaches(ctx context.Context, repo service.WarmRepository, c *cache.Cache, logger *slog.Logger) {
	if repo == nil || c == nil {
		return
	}

	projects := service.NewProjectService(repo)
	settings := service.NewSettingsService(repo)

	go warmEntry(ctx, c, logger, "projects:stats:24h", projectsStatsFresh, projectsStatsStale,
		func(ctx context.Context) (any, error) {
			return projects.UsageStats(24)
		})

	go warmEntry(ctx, c, logger, "stats:counts", statsCountsFresh, statsCountsStale,
		func(ctx context.Context) (any, error) {
			return settings.DBCounts()
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
		defer observability.RecoverAndReport("warm-login", false)
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
	defer observability.RecoverAndReport("warm-entry", false)
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

// dashboardWarmPeriod matches the coarse dashboard snapping grain: the cache
// key for the landing view changes once per period, so one warm per period
// keeps the first viewer of each period off the cold scans.
const dashboardWarmPeriod = 15 * time.Minute

// dashboardWarmSettle is how long after a period boundary the warm starts, so
// the data of the previous period has been ingested.
const dashboardWarmSettle = 3 * time.Second

// nextDashboardWarm returns when the warm for the period after now should run.
// jitter spreads pods so one of them computes and the others find the entry.
func nextDashboardWarm(now time.Time, jitter time.Duration) time.Time {
	return now.Truncate(dashboardWarmPeriod).Add(dashboardWarmPeriod + dashboardWarmSettle + jitter)
}

// WarmDashboardLoop warms the dashboard's landing view once at start and then
// once per period, until ctx is cancelled. Failures log at warn: a missed warm
// costs the next viewer a slow load and loses no data.
func WarmDashboardLoop(ctx context.Context, qs *service.QueryService, logger *slog.Logger) {
	if qs == nil {
		return
	}
	go func() {
		defer observability.RecoverAndReport("warm-dashboard", false)
		warm := func() {
			start := time.Now()
			wctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if err := qs.WarmDashboard(wctx, start); err != nil {
				if ctx.Err() == nil {
					logger.Warn("warm dashboard: failed", "elapsed", time.Since(start), "error", err)
				}
				return
			}
			logger.Info("warm dashboard: complete", "elapsed", time.Since(start))
		}
		warm()
		for {
			jitter := time.Duration(rand.Int64N(int64(20 * time.Second)))
			timer := time.NewTimer(time.Until(nextDashboardWarm(time.Now(), jitter)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				warm()
			}
		}
	}()
}
