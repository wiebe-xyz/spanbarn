package api

import (
	"net/http"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
)

type metricTotalsResponse struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Unit   string          `json:"unit"`
	Groups []metrics.Total `json:"groups"`
}

// handleMetricTotals returns how much a counter grew over the range, summed per
// group_by label set, largest first. It answers "what did each session cost"
// where the series endpoint answers "how fast".
//
// GET /api/v1/metrics/totals?name=<name>&project_id=1&from=<rfc3339>&to=<rfc3339>&group_by=session.id&label[repo]=foo
func (h *metricsQueryHandlers) handleMetricTotals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}
	q, ok := h.loadMetricQuery(w, r, 10000)
	if !ok {
		return
	}
	groups := metrics.Totals(q.metricType, q.in, q.groupBy)
	if groups == nil {
		groups = []metrics.Total{}
	}
	writeJSON(w, http.StatusOK, metricTotalsResponse{Name: q.name, Type: q.metricType, Unit: q.unit, Groups: groups})
}
