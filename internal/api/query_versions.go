package api

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"
)

// handleServiceVersions handles GET /api/v1/services/{service}/versions: the
// error rate and latency of a service per service.version, newest first.
func (h *queryHandlers) handleServiceVersions(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.service_versions")
	defer span.End()

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}
	svcName := pathParam(r.URL.Path, "service")
	if svcName == "" {
		writeError(w, http.StatusBadRequest, "missing service", "")
		return
	}
	span.SetAttributes(attribute.String("service", svcName))

	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return
	}

	versions, err := h.svc.ListServiceVersions(ctx, parseInt64Param(r, "project_id", 0), svcName, from, to)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}
	writeJSON(w, http.StatusOK, versions)
}
