package api

import (
	"context"
	"net/http"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/service"
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
	req, ok := parseMetricRequest(w, r, 10000)
	if !ok {
		return
	}
	// The same page asks for several metrics and a reload asks again, each a
	// scan of up to the limit's worth of points, so the result is kept per
	// window like the service reads are.
	resp, err := service.ReadMetricRange(r.Context(), h.svc, req.cacheKey("totals"), req.from, req.to,
		func(ctx context.Context, from, to time.Time) (metricTotalsResponse, error) {
			q, err := h.readMetricQuery(ctx, req, from, to)
			if err != nil {
				return metricTotalsResponse{}, err
			}
			groups := metrics.Totals(q.metricType, q.in, q.groupBy)
			if groups == nil {
				groups = []metrics.Total{}
			}
			return metricTotalsResponse{Name: q.name, Type: q.metricType, Unit: q.unit, Groups: groups}, nil
		})
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
