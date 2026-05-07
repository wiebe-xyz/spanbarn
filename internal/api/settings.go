package api

import (
	"encoding/json"
	"net/http"
	"runtime"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type settingsHandlers struct {
	repo     *repository.Repository
	dbPath   string
	spoolDir string
}

func (h *settingsHandlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")

	switch {
	case path == "/api/v1/settings" && r.Method == http.MethodGet:
		h.handleGetSettings(w, r)
	case path == "/api/v1/settings" && r.Method == http.MethodPut:
		h.handleUpdateSettings(w, r)
	case path == "/api/v1/stats" && r.Method == http.MethodGet:
		h.handleStats(w, r)
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
		"retention_full_hours":      true,
		"retention_aggregated_days": true,
		"retention_error_days":      true,
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

type statsResponse struct {
	DB     *repository.DBStats `json:"db"`
	Memory memoryStats         `json:"memory"`
}

type memoryStats struct {
	AllocBytes uint64 `json:"allocBytes"`
	SysBytes   uint64 `json:"sysBytes"`
	NumGC      uint32 `json:"numGC"`
}

func (h *settingsHandlers) handleStats(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.stats")
	defer span.End()

	dbStats, err := h.repo.GetDBStats(h.dbPath, h.spoolDir)
	if err != nil {
		writeServerError(w, r, "failed to read stats", err)
		return
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	writeJSON(w, http.StatusOK, statsResponse{
		DB: dbStats,
		Memory: memoryStats{
			AllocBytes: mem.Alloc,
			SysBytes:   mem.Sys,
			NumGC:      mem.NumGC,
		},
	})
}
