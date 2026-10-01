package api

import (
	"errors"
	"net/http"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// parseDashboardQuery reads the filter every dashboard endpoint shares. It
// writes the error response itself and reports ok=false when the request is
// not a valid GET.
func parseDashboardQuery(w http.ResponseWriter, r *http.Request) (service.DashboardQuery, bool) {
	from, to, ok := parseGetTimeRange(w, r)
	if !ok {
		return service.DashboardQuery{}, false
	}
	q := r.URL.Query()
	return service.DashboardQuery{
		ProjectID: parseInt64Param(r, "project_id", 0),
		Service:   q.Get("service"),
		Name:      q.Get("name"),
		Status:    q.Get("status"),
		From:      from,
		To:        to,
	}, true
}

// writeDashboardError maps a dashboard service error to an HTTP response.
func writeDashboardError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, service.ErrInvalidDashboardRequest) {
		writeError(w, http.StatusBadRequest, "invalid dashboard request", err.Error())
		return
	}
	writeServerError(w, r, "query failed", err)
}

// groupByParam returns the group_by query value, defaulting to service.
func groupByParam(r *http.Request) string {
	if g := r.URL.Query().Get("group_by"); g != "" {
		return g
	}
	return "service"
}

func (h *queryHandlers) handleDashboardCounts(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.dashboard_counts")
	defer span.End()

	q, ok := parseDashboardQuery(w, r)
	if !ok {
		return
	}
	res, err := h.svc.GetDashboardCounts(ctx, q, groupByParam(r), r.URL.Query().Get("root_only") == "true")
	if err != nil {
		writeDashboardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *queryHandlers) handleDashboardPercentiles(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.dashboard_percentiles")
	defer span.End()

	q, ok := parseDashboardQuery(w, r)
	if !ok {
		return
	}
	res, err := h.svc.GetDashboardPercentiles(ctx, q, groupByParam(r))
	if err != nil {
		writeDashboardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *queryHandlers) handleDashboardHeatmap(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.dashboard_heatmap")
	defer span.End()

	q, ok := parseDashboardQuery(w, r)
	if !ok {
		return
	}
	res, err := h.svc.GetDashboardHeatmap(ctx, q, r.URL.Query().Get("root_only") == "true")
	if err != nil {
		writeDashboardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
