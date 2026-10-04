package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

type sloHandlers struct {
	svc *service.SLOService
}

// register mounts the SLO endpoints. All of them need a session.
func (h *sloHandlers) register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/slos":                               h.handleList,
		"POST /api/v1/slos":                              h.handleCreate,
		"GET /api/v1/slos/{id}":                          h.handleGet,
		"PUT /api/v1/slos/{id}":                          h.handleUpdate,
		"DELETE /api/v1/slos/{id}":                       h.handleDelete,
		"GET /api/v1/slos/{id}/status":                   h.handleStatus,
		"GET /api/v1/slos/{id}/burn-alerts":              h.handleListAlerts,
		"POST /api/v1/slos/{id}/burn-alerts":             h.handleCreateAlert,
		"PUT /api/v1/slos/{id}/burn-alerts/{alertId}":    h.handleUpdateAlert,
		"DELETE /api/v1/slos/{id}/burn-alerts/{alertId}": h.handleDeleteAlert,
	}
	for pattern, fn := range routes {
		mux.Handle(pattern, wrap(fn))
	}
}

// sloProject reads the project of the request: the one a project-scoped key
// pinned in the context, else the project_id query parameter. A missing
// project is a 400 so that an id is never looked up across projects.
func sloProject(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id := GetProjectID(r.Context())
	if id == 0 {
		id = parseInt64Param(r, "project_id", 0)
	}
	if id <= 0 {
		writeError(w, http.StatusBadRequest, "project_id is required", "")
		return 0, false
	}
	return id, true
}

// writeSLOError maps service errors to HTTP statuses.
func writeSLOError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found", "")
	case errors.Is(err, service.ErrConflict):
		writeError(w, http.StatusConflict, "an SLO with this name already exists", "")
	case errors.Is(err, filter.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid request", err.Error())
	default:
		writeServerError(w, r, "slo request failed", err)
	}
}

// sloRoute is the project plus the SLO id of a request.
func sloRoute(w http.ResponseWriter, r *http.Request) (project, id int64, ok bool) {
	if project, ok = sloProject(w, r); !ok {
		return 0, 0, false
	}
	if id, ok = pathID(w, r, "id"); !ok {
		return 0, 0, false
	}
	return project, id, true
}

type sloBody struct {
	Name        string          `json:"name"`
	GoodFilter  json.RawMessage `json:"goodFilter"`
	TotalFilter json.RawMessage `json:"totalFilter"`
	Target      float64         `json:"target"`
	WindowDays  int             `json:"windowDays"`
}

func (b sloBody) input() service.SLOInput {
	return service.SLOInput{
		Name: b.Name, GoodFilter: b.GoodFilter, TotalFilter: b.TotalFilter,
		Target: b.Target, WindowDays: b.WindowDays,
	}
}

func (h *sloHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	project, ok := sloProject(w, r)
	if !ok {
		return
	}
	slos, err := h.svc.ListSLOs(project)
	if err != nil {
		writeSLOError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, slos)
}

func (h *sloHandlers) handleCreate(w http.ResponseWriter, r *http.Request) {
	project, ok := sloProject(w, r)
	if !ok {
		return
	}
	var b sloBody
	if !decodeBody(w, r, &b) {
		return
	}
	id, err := h.svc.CreateSLO(project, b.input())
	if err != nil {
		writeSLOError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (h *sloHandlers) handleGet(w http.ResponseWriter, r *http.Request) {
	project, id, ok := sloRoute(w, r)
	if !ok {
		return
	}
	slo, err := h.svc.GetSLO(project, id)
	if err != nil {
		writeSLOError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, slo)
}

func (h *sloHandlers) handleUpdate(w http.ResponseWriter, r *http.Request) {
	project, id, ok := sloRoute(w, r)
	if !ok {
		return
	}
	var b sloBody
	if !decodeBody(w, r, &b) {
		return
	}
	if err := h.svc.UpdateSLO(project, id, b.input()); err != nil {
		writeSLOError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *sloHandlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	project, id, ok := sloRoute(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteSLO(project, id); err != nil {
		writeSLOError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *sloHandlers) handleStatus(w http.ResponseWriter, r *http.Request) {
	project, id, ok := sloRoute(w, r)
	if !ok {
		return
	}
	st, err := h.svc.Status(project, id)
	if err != nil {
		writeSLOError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
