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

	allowed := map[string]bool{
		"retention_full_hours":        true,
		"retention_interesting_hours": true,
		"retention_aggregated_days":   true,
		"retention_error_days":        true,
		"ingest_sample_rate":          true,
	}

	for k, v := range body {
		if !allowed[k] {
			writeError(w, http.StatusBadRequest, "unknown setting: "+k, "")
			return
		}
		if err := h.repo.SetSetting(k, v); err != nil {
			writeServerError(w, r, "failed to save setting", err)
			return
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

// serveSWR serves a cached value, computing it inline on miss and refreshing
// in the background on stale hit.
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
				if _, already := statsRevalidating.LoadOrStore(key, true); !already {
					go func() {
						defer statsRevalidating.Delete(key)
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if v, err := compute(ctx); err == nil {
							cache.SetSWR(c, ctx, key, v, freshTTL, staleTTL)
						}
					}()
				}
			}
			writeJSON(w, http.StatusOK, value)
			return
		}
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
