package api

import "net/http"

func (h *queryHandlers) handleSpanLedger(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.span_ledger")
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

	entries, err := h.svc.SpanLedger(ctx, parseInt64Param(r, "project_id", 0), from, to)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}

	writeJSON(w, http.StatusOK, entries)
}
