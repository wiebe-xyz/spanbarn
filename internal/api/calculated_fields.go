package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

type calculatedFieldHandlers struct {
	svc *service.CalculatedFieldService
}

// register mounts the calculated field endpoints. All of them need a session.
func (h *calculatedFieldHandlers) register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/calculated-fields":          h.handleList,
		"POST /api/v1/calculated-fields":         h.handleCreate,
		"POST /api/v1/calculated-fields/preview": h.handlePreview,
		"PUT /api/v1/calculated-fields/{id}":     h.handleUpdate,
		"DELETE /api/v1/calculated-fields/{id}":  h.handleDelete,
	}
	for pattern, fn := range routes {
		mux.Handle(pattern, wrap(fn))
	}
}

// newCalculatedFieldHandlers wires the handlers to the service over repo.
func newCalculatedFieldHandlers(repo service.CalculatedFieldRepository) *calculatedFieldHandlers {
	return &calculatedFieldHandlers{svc: service.NewCalculatedFieldService(repo, slog.Default())}
}

// writeCalculatedFieldError maps service errors to HTTP statuses.
func writeCalculatedFieldError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found", "")
	case errors.Is(err, filter.ErrInvalid):
		// The editor shows the message, so it carries the reason.
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), filter.ErrInvalid.Error()+": "), "")
	default:
		writeServerError(w, r, "calculated field request failed", err)
	}
}

type calculatedFieldBody struct {
	ProjectID  int64  `json:"projectId"`
	Name       string `json:"name"`
	Expression string `json:"expression"`
}

func (h *calculatedFieldHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	fields, err := h.svc.List(parseInt64Param(r, "project_id", 0))
	if err != nil {
		writeCalculatedFieldError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, fields)
}

func (h *calculatedFieldHandlers) handleCreate(w http.ResponseWriter, r *http.Request) {
	var b calculatedFieldBody
	if !decodeBody(w, r, &b) {
		return
	}
	id, err := h.svc.Create(b.ProjectID, b.Name, b.Expression)
	if err != nil {
		writeCalculatedFieldError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (h *calculatedFieldHandlers) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b calculatedFieldBody
	if !decodeBody(w, r, &b) {
		return
	}
	if err := h.svc.Update(id, b.Name, b.Expression); err != nil {
		writeCalculatedFieldError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *calculatedFieldHandlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(id); err != nil {
		writeCalculatedFieldError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePreview evaluates an unsaved expression on the project's newest spans.
func (h *calculatedFieldHandlers) handlePreview(w http.ResponseWriter, r *http.Request) {
	var b calculatedFieldBody
	if !decodeBody(w, r, &b) {
		return
	}
	samples, err := h.svc.Preview(r.Context(), b.ProjectID, b.Name, b.Expression)
	if err != nil {
		writeCalculatedFieldError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": samples})
}
