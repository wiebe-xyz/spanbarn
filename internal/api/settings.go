package api

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type settingsHandlers struct {
	repo     *repository.Repository
	dbPath   string
	spoolDir string
	cache    *cache.Cache
}

// revalidating tracks SWR background refreshes so we don't kick off multiple
// concurrent recomputes for the same key.
var statsRevalidating sync.Map

func (h *settingsHandlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")

	switch {
	case path == "/api/v1/settings" && r.Method == http.MethodGet:
		h.handleGetSettings(w, r)
	case path == "/api/v1/settings" && r.Method == http.MethodPut:
		h.handleUpdateSettings(w, r)
	case path == "/api/v1/stats/db-size" && r.Method == http.MethodGet:
		h.handleStatsDBSize(w, r)
	case path == "/api/v1/stats/counts" && r.Method == http.MethodGet:
		h.handleStatsCounts(w, r)
	case path == "/api/v1/stats/runtime" && r.Method == http.MethodGet:
		h.handleStatsRuntime(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
	}
}

func (h *settingsHandlers) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.settings.get")
	defer span.End()

	settings, err := h.repo.GetAllSettings()
	if err != nil {
		writeServerError(w, r, "failed to read settings", err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *settingsHandlers) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.settings.update")
	defer span.End()

	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
		return
	}

	for k, v := range body {
		if !isAllowedSettingKey(k) {
			writeError(w, http.StatusBadRequest, "unknown setting: "+k, "")
			return
		}
		if v == "" {
			if err := h.repo.DeleteSetting(k); err != nil {
				writeServerError(w, r, "failed to delete setting", err)
				return
			}
		} else {
			if err := h.repo.SetSetting(k, v); err != nil {
				writeServerError(w, r, "failed to save setting", err)
				return
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type runtimeStats struct {
	AllocBytes uint64 `json:"allocBytes"`
	SysBytes   uint64 `json:"sysBytes"`
	NumGC      uint32 `json:"numGC"`
}

const (
	statsDBSizeFresh = 5 * time.Minute
	statsDBSizeStale = 30 * time.Minute
	statsCountsFresh = 1 * time.Hour
	statsCountsStale = 24 * time.Hour
)

// kickBackgroundRefresh runs `compute` in a goroutine (deduplicated by key)
// and writes the result into the cache. The goroutine uses a fresh context
// detached from the request so it survives the HTTP write timeout.
func kickBackgroundRefresh[T any](
	c *cache.Cache,
	key string,
	freshTTL, staleTTL time.Duration,
	compute func(ctx context.Context) (T, error),
) {
	if _, already := statsRevalidating.LoadOrStore(key, true); already {
		return
	}
	go func() {
		defer statsRevalidating.Delete(key)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if v, err := compute(ctx); err == nil {
			cache.SetSWR(c, ctx, key, v, freshTTL, staleTTL)
		}
	}()
}

// serveSWR serves a cached value if present, otherwise computes inline.
// Stale hits trigger a background refresh. Cache misses ALSO trigger a
// background refresh so the cache gets populated even if the inline compute
// is killed by the HTTP WriteTimeout — the next request will then hit cache.
func serveSWR[T any](
	w http.ResponseWriter,
	r *http.Request,
	c *cache.Cache,
	key string,
	freshTTL, staleTTL time.Duration,
	compute func(ctx context.Context) (T, error),
) {
	if c != nil {
		if value, found, fresh := cache.GetSWR[T](c, r.Context(), key); found {
			if !fresh {
				kickBackgroundRefresh(c, key, freshTTL, staleTTL, compute)
			}
			writeJSON(w, http.StatusOK, value)
			return
		}
		kickBackgroundRefresh(c, key, freshTTL, staleTTL, compute)
	}

	value, err := compute(r.Context())
	if err != nil {
		writeServerError(w, r, "failed to compute stats", err)
		return
	}
	if c != nil {
		cache.SetSWR(c, r.Context(), key, value, freshTTL, staleTTL)
	}
	writeJSON(w, http.StatusOK, value)
}

// isAllowedSettingKey returns true for writable settings keys.
// Retention keys are plain identifiers; sampling keys use a dot-namespaced
// hierarchy: ingest.sample_ratio.default, ingest.sample_ratio.project.{id},
// ingest.sample_ratio.project.{id}.op.{operation}.
// Boring span keys: boring.sample_ratio, boring.sample_ratio.project.{id},
// boring_retention_minutes, boring.verbose_until.project.{id},
// boring.min_traces_per_minute, boring.min_traces_per_minute.project.{id}.
func isAllowedSettingKey(k string) bool {
	switch k {
	case "retention_full_hours", "retention_interesting_hours",
		"retention_aggregated_days", "retention_error_days",
		"boring_retention_minutes", "boring.sample_ratio",
		"boring.min_traces_per_minute",
		"metrics_retention_days", "log_retention_hours", "error_log_retention_days":
		return true
	}
	return strings.HasPrefix(k, "ingest.sample_ratio.") ||
		strings.HasPrefix(k, "boring.sample_ratio.") ||
		strings.HasPrefix(k, "boring.min_traces_per_minute.") ||
		strings.HasPrefix(k, "boring.verbose_until.")
}

func (h *settingsHandlers) handleStatsDBSize(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.stats.db_size")
	defer span.End()

	serveSWR(w, r, h.cache, "stats:db-size", statsDBSizeFresh, statsDBSizeStale,
		func(_ context.Context) (*repository.DBSize, error) {
			return h.repo.GetDBSize(h.dbPath, h.spoolDir)
		})
}

func (h *settingsHandlers) handleStatsCounts(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.stats.counts")
	defer span.End()

	serveSWR(w, r, h.cache, "stats:counts", statsCountsFresh, statsCountsStale,
		func(_ context.Context) (*repository.DBCounts, error) {
			return h.repo.GetDBCounts()
		})
}

func (h *settingsHandlers) handleStatsRuntime(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.stats.runtime")
	defer span.End()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	writeJSON(w, http.StatusOK, runtimeStats{
		AllocBytes: mem.Alloc,
		SysBytes:   mem.Sys,
		NumGC:      mem.NumGC,
	})
}
