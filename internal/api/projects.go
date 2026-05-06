package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type projectHandlers struct {
	repo *repository.Repository
}

func (h *projectHandlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/projects")
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		if r.Method == http.MethodGet {
			h.handleList(w, r)
			return
		}
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	parts := strings.SplitN(path, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project id", "")
		return
	}

	if len(parts) == 2 && parts[1] == "approve" {
		if r.Method == http.MethodPost {
			h.handleApprove(w, r, id)
			return
		}
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	writeError(w, http.StatusNotFound, "not found", "")
}

func (h *projectHandlers) handleList(w http.ResponseWriter, _ *http.Request) {
	projects, err := h.repo.ListProjects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list projects", "")
		return
	}
	if projects == nil {
		projects = []repository.Project{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(projects)
}

func (h *projectHandlers) handleApprove(w http.ResponseWriter, _ *http.Request, id int64) {
	project, err := h.repo.ApproveProject(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found", "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(project)
}
