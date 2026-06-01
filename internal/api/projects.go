package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const (
	projectsStatsFresh = 1 * time.Hour
	projectsStatsStale = 24 * time.Hour
)

type projectHandlers struct {
	repo  *repository.Repository
	cache *cache.Cache
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

	if path == "stats" && r.Method == http.MethodGet {
		h.handleStats(w, r)
		return
	}

	parts := strings.SplitN(path, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project id", "")
		return
	}

	if len(parts) == 1 {
		if r.Method == http.MethodDelete {
			h.handleDelete(w, r, id)
			return
		}
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	switch parts[1] {
	case "approve":
		if r.Method == http.MethodPost {
			h.handleApprove(w, r, id)
			return
		}
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	case "apikeys":
		if r.Method == http.MethodGet {
			h.handleListAPIKeys(w, r, id)
			return
		}
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	case "e2e":
		switch r.Method {
		case http.MethodPost:
			h.handleEnableE2E(w, r, id)
		case http.MethodDelete:
			h.handleDisableE2E(w, r, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		}
		return
	}

	writeError(w, http.StatusNotFound, "not found", "")
}

func (h *projectHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.projects.list")
	defer span.End()

	projects, err := h.repo.ListProjects()
	if err != nil {
		writeServerError(w, r, "failed to list projects", err)
		return
	}
	if projects == nil {
		projects = []repository.Project{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(projects)
}

func (h *projectHandlers) handleDelete(w http.ResponseWriter, r *http.Request, id int64) {
	_, span := apiTracer.Start(r.Context(), "api.projects.delete")
	defer span.End()
	span.SetAttributes(attribute.Int64("project.id", id))

	if err := h.repo.DeleteProject(id); err != nil {
		writeError(w, http.StatusNotFound, "project not found", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *projectHandlers) handleApprove(w http.ResponseWriter, r *http.Request, id int64) {
	_, span := apiTracer.Start(r.Context(), "api.projects.approve")
	defer span.End()
	span.SetAttributes(attribute.Int64("project.id", id))

	project, err := h.repo.ApproveProject(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found", "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(project)
}

func (h *projectHandlers) handleStats(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.projects.stats")
	defer span.End()

	serveSWR(w, r, h.cache, "projects:stats:24h", projectsStatsFresh, projectsStatsStale,
		func(_ context.Context) ([]repository.ProjectUsageStats, error) {
			stats, err := h.repo.ProjectUsageStatsAll(24)
			if err != nil {
				return nil, err
			}
			if stats == nil {
				stats = []repository.ProjectUsageStats{}
			}
			return stats, nil
		})
}

type apiKeyResponse struct {
	ID         int64   `json:"id"`
	ProjectID  int64   `json:"projectId"`
	Name       string  `json:"name"`
	Scope      string  `json:"scope"`
	LastUsedAt *string `json:"lastUsedAt"`
	CreatedAt  string  `json:"createdAt"`
}

func (h *projectHandlers) handleListAPIKeys(w http.ResponseWriter, r *http.Request, projectID int64) {
	_, span := apiTracer.Start(r.Context(), "api.projects.list_api_keys")
	defer span.End()
	span.SetAttributes(attribute.Int64("project.id", projectID))

	keys, err := h.repo.ListAPIKeys(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list api keys", "")
		return
	}
	out := make([]apiKeyResponse, 0, len(keys))
	for _, k := range keys {
		r := apiKeyResponse{
			ID:        k.ID,
			ProjectID: k.ProjectID,
			Name:      k.Name,
			Scope:     k.Scope,
			CreatedAt: k.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if k.LastUsedAt.Valid {
			t := k.LastUsedAt.Time.Format("2006-01-02T15:04:05Z")
			r.LastUsedAt = &t
		}
		out = append(out, r)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (h *projectHandlers) handleEnableE2E(w http.ResponseWriter, r *http.Request, id int64) {
	_, span := apiTracer.Start(r.Context(), "api.projects.enable_e2e")
	defer span.End()
	span.SetAttributes(attribute.Int64("project.id", id))

	if err := h.repo.SetProjectE2E(id, true); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enable e2e", "")
		return
	}
	p, err := h.repo.GetProjectByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load project", "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func (h *projectHandlers) handleDisableE2E(w http.ResponseWriter, r *http.Request, id int64) {
	_, span := apiTracer.Start(r.Context(), "api.projects.disable_e2e")
	defer span.End()
	span.SetAttributes(attribute.Int64("project.id", id))

	if err := h.repo.SetProjectE2E(id, false); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to disable e2e", "")
		return
	}
	p, err := h.repo.GetProjectByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load project", "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}
