package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type savedQueryHandlers struct {
	repo *repository.Repository
}

func (h *savedQueryHandlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")

	switch {
	case path == "/api/v1/saved-queries" && r.Method == http.MethodGet:
		h.handleList(w, r)
	case path == "/api/v1/saved-queries" && r.Method == http.MethodPost:
		h.handleCreate(w, r)
	case strings.HasPrefix(path, "/api/v1/saved-queries/") && r.Method == http.MethodDelete:
		h.handleDelete(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
	}
}

func (h *savedQueryHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	projectID := parseInt64Param(r, "project_id", 1)
	queries, err := h.repo.ListSavedQueries(projectID)
	if err != nil {
		writeServerError(w, r, "failed to list saved queries", err)
		return
	}
	if queries == nil {
		queries = []repository.SavedQuery{}
	}
	writeJSON(w, http.StatusOK, queries)
}

func (h *savedQueryHandlers) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID     int64  `json:"projectId"`
		Name          string `json:"name"`
		Service       string `json:"service"`
		Operation     string `json:"operation"`
		Status        string `json:"status"`
		MinDurationUs int64  `json:"minDurationUs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required", "")
		return
	}
	if body.ProjectID == 0 {
		body.ProjectID = 1
	}

	id, err := h.repo.CreateSavedQuery(repository.SavedQuery{
		ProjectID:     body.ProjectID,
		Name:          body.Name,
		Service:       body.Service,
		Operation:     body.Operation,
		Status:        body.Status,
		MinDurationUs: body.MinDurationUs,
	})
	if err != nil {
		writeServerError(w, r, "failed to create saved query", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (h *savedQueryHandlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		writeError(w, http.StatusBadRequest, "missing id", "")
		return
	}
	id, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id", "")
		return
	}
	if err := h.repo.DeleteSavedQuery(id); err != nil {
		writeServerError(w, r, "failed to delete saved query", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
