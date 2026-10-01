package api

import (
	"errors"
	"net/http"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// parseStructureFilter reads the has_root and orphans trace list filters.
// has_root accepts "true" or "false"; anything else applies no filter. orphans
// is true for traces with at least one orphan span (orphan_count > 0).
func parseStructureFilter(r *http.Request) (hasRoot *bool, orphans bool) {
	q := r.URL.Query()
	switch q.Get("has_root") {
	case "true":
		v := true
		hasRoot = &v
	case "false":
		v := false
		hasRoot = &v
	}
	return hasRoot, q.Get("orphans") == "true"
}

// parseTraceHealthQuery reads the project and time range every trace health
// view requires. It writes the error response itself and reports ok=false when
// the request is not a valid GET.
func parseTraceHealthQuery(w http.ResponseWriter, r *http.Request) (service.TraceHealthQuery, bool) {
	from, to, ok := parseGetTimeRange(w, r)
	if !ok {
		return service.TraceHealthQuery{}, false
	}
	return service.TraceHealthQuery{
		ProjectID: parseInt64Param(r, "project_id", 0),
		From:      from,
		To:        to,
		Limit:     parseIntParam(r, "limit", 0),
	}, true
}

func writeTraceHealthError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, service.ErrInvalidTraceHealthRequest) {
		writeError(w, http.StatusBadRequest, "invalid trace health request", err.Error())
		return
	}
	writeServerError(w, r, "query failed", err)
}

func (h *queryHandlers) handleOrphanSpans(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.orphan_spans")
	defer span.End()

	q, ok := parseTraceHealthQuery(w, r)
	if !ok {
		return
	}
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID))
	res, err := h.svc.ListOrphanSpans(ctx, q)
	if err != nil {
		writeTraceHealthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *queryHandlers) handleRootlessTraces(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.rootless_traces")
	defer span.End()

	q, ok := parseTraceHealthQuery(w, r)
	if !ok {
		return
	}
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID))
	res, err := h.svc.ListRootlessTraces(ctx, q)
	if err != nil {
		writeTraceHealthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *queryHandlers) handleSingleSpanTraces(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.single_span_traces")
	defer span.End()

	q, ok := parseTraceHealthQuery(w, r)
	if !ok {
		return
	}
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID))
	res, err := h.svc.ListSingleSpanTraces(ctx, q)
	if err != nil {
		writeTraceHealthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *queryHandlers) handleSpanNames(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.span_names")
	defer span.End()

	q, ok := parseTraceHealthQuery(w, r)
	if !ok {
		return
	}
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID))
	res, err := h.svc.ListSpanNames(ctx, q)
	if err != nil {
		writeTraceHealthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
