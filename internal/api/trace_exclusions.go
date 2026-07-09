package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type traceExclusionHandlers struct {
	repo *repository.Repository
}

func (h *traceExclusionHandlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	// /api/v1/trace-exclusions         => len 3
	// /api/v1/trace-exclusions/42      => len 4
	switch {
	case len(parts) == 3:
		switch r.Method {
		case http.MethodGet:
			h.handleList(w, r)
		case http.MethodPost:
			h.handleCreate(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		}
	case len(parts) == 4:
		id, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id", "")
			return
		}
		if r.Method == http.MethodDelete {
			h.handleDelete(w, r, id)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		}
	default:
		writeError(w, http.StatusNotFound, "not found", "")
	}
}

func (h *traceExclusionHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	projectID := parseInt64Param(r, "project_id", 1)
	exclusions, err := h.repo.ListTraceExclusions(projectID)
	writeListJSON(w, r, "trace exclusions", exclusions, err)
}

func (h *traceExclusionHandlers) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID int64  `json:"projectId"`
		Operation string `json:"operation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
		return
	}
	if req.Operation == "" {
		writeError(w, http.StatusBadRequest, "operation is required", "")
		return
	}
	if req.ProjectID == 0 {
		req.ProjectID = 1
	}
	id, err := h.repo.CreateTraceExclusion(req.ProjectID, req.Operation)
	if err != nil {
		writeServerError(w, r, "failed to create trace exclusion", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (h *traceExclusionHandlers) handleDelete(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.repo.DeleteTraceExclusion(id); err != nil {
		writeServerError(w, r, "failed to delete trace exclusion", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
