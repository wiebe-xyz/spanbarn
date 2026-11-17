package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
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
	req.totals = true
	// The same page asks for several metrics and a reload asks again, each a
	// scan of up to the limit's worth of points, so the result is kept per
	// window like the service reads are.
	started := time.Now()
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
	logSlowTotals(slog.Default(), req, time.Since(started), len(resp.Groups))
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// slowTotalsThreshold is the read time from which a totals request is logged.
// The route latency metric carries the route only, so without this line a slow
// totals read cannot be traced to a project or metric (#457).
const slowTotalsThreshold = 2 * time.Second

// logSlowTotals names a slow totals read: the project, metric, range, group-by
// keys and label keys, never a label value. Cache hits return in microseconds,
// so a line means a read that scanned.
func logSlowTotals(log *slog.Logger, req metricRequest, took time.Duration, groups int) {
	if took < slowTotalsThreshold {
		return
	}
	labelKeys := make([]string, 0, len(req.labels))
	for k := range req.labels {
		labelKeys = append(labelKeys, k)
	}
	log.Info("slow metric totals read",
		"project_id", req.projectID,
		"metric", req.name,
		"range_hours", req.to.Sub(req.from).Hours(),
		"group_by", strings.Join(req.groupBy, ","),
		"label_keys", strings.Join(labelKeys, ","),
		"environment", req.environment,
		"groups", groups,
		"took_ms", took.Milliseconds(),
	)
}
