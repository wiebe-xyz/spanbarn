package api

import (
	"net/http"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
)

type metricInsightsResponse struct {
	Insights []metrics.Insight `json:"insights"`
}

// handleMetricInsights scans every metric series in the range (via rollups) and
// returns the most notable changes: spikes, drops, percentile regressions and
// newly-appeared series, so users don't have to eyeball every graph.
//
// GET /api/v1/metrics/insights?project_id=1&from=<rfc3339>&to=<rfc3339>
func (h *metricsQueryHandlers) handleMetricInsights(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	projectID := parseInt64Param(r, "project_id", 0)
	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return
	}
	if from.IsZero() {
		from = time.Now().Add(-24 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}

	insights, err := h.svc.Insights(r.Context(), projectID, from, to)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}
	writeJSON(w, http.StatusOK, metricInsightsResponse{Insights: insights})
}
