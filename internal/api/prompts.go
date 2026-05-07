package api

import "net/http"

func (h *queryHandlers) handlePrompts(w http.ResponseWriter, r *http.Request) {
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

	prompts, err := h.svc.ListPrompts(r.Context(), 0, from, to, svcFilter, modelFilter)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}

	writeJSON(w, http.StatusOK, prompts)
}

func (h *queryHandlers) handlePromptDetail(w http.ResponseWriter, r *http.Request) {
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

	if name == "" {
		writeError(w, http.StatusBadRequest, "missing name parameter", "")
		return
	}

	records, err := h.svc.GetPromptDetail(r.Context(), 0, from, to, name, model, svcFilter)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}

	writeJSON(w, http.StatusOK, records)
}
