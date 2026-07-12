package api

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"
)

func (h *queryHandlers) handlePrompts(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.prompts")
	defer span.End()

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return
	}

	svcFilter := r.URL.Query().Get("service")
	modelFilter := r.URL.Query().Get("model")

	prompts, err := h.svc.ListPrompts(ctx, parseInt64Param(r, "project_id", 0), from, to, svcFilter, modelFilter)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}

	writeJSON(w, http.StatusOK, prompts)
}

func (h *queryHandlers) handlePromptDetail(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.prompt_detail")
	defer span.End()

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return
	}

	name := r.URL.Query().Get("name")
	model := r.URL.Query().Get("model")
	svcFilter := r.URL.Query().Get("service")
	statusFilter := r.URL.Query().Get("status")
	finishReason := r.URL.Query().Get("finish_reason")

	if name == "" {
		writeError(w, http.StatusBadRequest, "missing name parameter", "")
		return
	}
	span.SetAttributes(attribute.String("prompt.name", name))

	records, err := h.svc.GetPromptDetail(ctx, parseInt64Param(r, "project_id", 0), from, to, name, model, svcFilter, statusFilter, finishReason)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}

	writeJSON(w, http.StatusOK, records)
}
