package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// alertHandlers holds session-authenticated alert endpoint handlers.
type alertHandlers struct {
	repo *repository.Repository
}

type alertRequest struct {
	ProjectID        int64   `json:"projectId"`
	Service          string  `json:"service"`
	Operation        string  `json:"operation"`
	Type             string  `json:"type"`
	Threshold        float64 `json:"threshold"`
	ComparisonWindow int     `json:"comparisonWindow"`
	CooldownMinutes  int     `json:"cooldownMinutes"`
	WebhookURL       string  `json:"webhookUrl"`
	Email            string  `json:"email"`
	Enabled          bool    `json:"enabled"`
}

type alertResponse struct {
	ID               int64   `json:"id"`
	ProjectID        int64   `json:"projectId"`
	Service          string  `json:"service"`
	Operation        string  `json:"operation"`
	Type             string  `json:"type"`
	Threshold        float64 `json:"threshold"`
	ComparisonWindow int     `json:"comparisonWindow"`
	CooldownMinutes  int     `json:"cooldownMinutes"`
	WebhookURL       string  `json:"webhookUrl"`
	Email            string  `json:"email"`
	Enabled          bool    `json:"enabled"`
	LastTriggeredAt  *string `json:"lastTriggeredAt,omitempty"`
	CreatedAt        string  `json:"createdAt"`
}

func toAlertResponse(a repository.Alert) alertResponse {
	resp := alertResponse{
		ID:               a.ID,
		ProjectID:        a.ProjectID,
		Service:          a.Service,
		Operation:        a.Operation,
		Type:             a.Type,
		Threshold:        a.Threshold,
		ComparisonWindow: a.ComparisonWindow,
		CooldownMinutes:  a.CooldownMinutes,
		WebhookURL:       a.WebhookURL,
		Email:            a.Email,
		Enabled:          a.Enabled,
		CreatedAt:        a.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if a.LastTriggeredAt.Valid {
		s := a.LastTriggeredAt.Time.Format("2006-01-02T15:04:05Z07:00")
		resp.LastTriggeredAt = &s
	}
	return resp
}

// ServeHTTP dispatches alert routes based on URL path and method.
func (h *alertHandlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	// /api/v1/alerts => parts = [api, v1, alerts]
	// /api/v1/alerts/123 => parts = [api, v1, alerts, 123]

	switch {
	case len(parts) == 3:
		// /api/v1/alerts
		switch r.Method {
		case http.MethodGet:
			h.handleList(w, r)
		case http.MethodPost:
			h.handleCreate(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		}
	case len(parts) == 4:
		// /api/v1/alerts/{id}
		id, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid alert ID", "")
			return
		}
		switch r.Method {
		case http.MethodPut:
			h.handleUpdate(w, r, id)
		case http.MethodDelete:
			h.handleDelete(w, r, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		}
	default:
		writeError(w, http.StatusNotFound, "not found", "")
	}
}

func (h *alertHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.alerts.list")
	defer span.End()

	projectID := parseInt64Param(r, "project_id", 0)

	alerts, err := h.repo.ListAlerts(projectID)
	if err != nil {
		writeServerError(w, r, "failed to list alerts", err)
		return
	}

	resp := make([]alertResponse, len(alerts))
	for i, a := range alerts {
		resp[i] = toAlertResponse(a)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *alertHandlers) handleCreate(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.alerts.create")
	defer span.End()

	var req alertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
		return
	}

	if req.ProjectID == 0 || req.Service == "" || req.Type == "" {
		writeError(w, http.StatusBadRequest, "projectId, service, and type are required", "")
		return
	}
	if req.Type != "latency" && req.Type != "error_rate" {
		writeError(w, http.StatusBadRequest, "type must be 'latency' or 'error_rate'", "")
		return
	}
	if req.ComparisonWindow <= 0 {
		req.ComparisonWindow = 10
	}
	if req.CooldownMinutes <= 0 {
		req.CooldownMinutes = 30
	}

	alert := repository.Alert{
		ProjectID:        req.ProjectID,
		Service:          req.Service,
		Operation:        req.Operation,
		Type:             req.Type,
		Threshold:        req.Threshold,
		ComparisonWindow: req.ComparisonWindow,
		CooldownMinutes:  req.CooldownMinutes,
		WebhookURL:       req.WebhookURL,
		Email:            req.Email,
		Enabled:          req.Enabled,
	}

	id, err := h.repo.CreateAlert(alert)
	if err != nil {
		writeServerError(w, r, "failed to create alert", err)
		return
	}

	// Fetch the created alert to return full response.
	alerts, err := h.repo.ListAlerts(req.ProjectID)
	if err != nil {
		writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
		return
	}
	for _, a := range alerts {
		if a.ID == id {
			writeJSON(w, http.StatusCreated, toAlertResponse(a))
			return
		}
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (h *alertHandlers) handleUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	_, span := apiTracer.Start(r.Context(), "api.alerts.update")
	defer span.End()
	span.SetAttributes(attribute.Int64("alert.id", id))

	var req alertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
		return
	}

	if req.Type != "" && req.Type != "latency" && req.Type != "error_rate" {
		writeError(w, http.StatusBadRequest, "type must be 'latency' or 'error_rate'", "")
		return
	}

	alert := repository.Alert{
		ID:               id,
		Service:          req.Service,
		Operation:        req.Operation,
		Type:             req.Type,
		Threshold:        req.Threshold,
		ComparisonWindow: req.ComparisonWindow,
		CooldownMinutes:  req.CooldownMinutes,
		WebhookURL:       req.WebhookURL,
		Email:            req.Email,
		Enabled:          req.Enabled,
	}

	if err := h.repo.UpdateAlert(alert); err != nil {
		writeServerError(w, r, "failed to update alert", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *alertHandlers) handleDelete(w http.ResponseWriter, r *http.Request, id int64) {
	_, span := apiTracer.Start(r.Context(), "api.alerts.delete")
	defer span.End()
	span.SetAttributes(attribute.Int64("alert.id", id))

	if err := h.repo.DeleteAlert(id); err != nil {
		writeServerError(w, r, "failed to delete alert", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
