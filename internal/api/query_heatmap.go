package api

import (
	"errors"
	"net/http"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// handleHeatmap returns the duration distribution of a filtered span selection
// over time, as counts per time bucket and log-scale duration bucket.
func (h *queryHandlers) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.heatmap")
	defer span.End()

	from, to, ok := parseGetTimeRange(w, r)
	if !ok {
		return
	}
	expr, ok := parseFilterNamed(w, r, "filter")
	if !ok {
		return
	}
	q := service.HeatmapQuery{
		ProjectID:       parseInt64Param(r, "project_id", 0),
		From:            from,
		To:              to,
		Filter:          expr,
		TimeBuckets:     parseIntParam(r, "time_buckets", 0),
		DurationBuckets: parseIntParam(r, "duration_buckets", 0),
		MaxSpans:        parseIntParam(r, "max_spans", 0),
	}
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID))
	res, err := h.svc.Heatmap(ctx, q)
	if err != nil {
		if errors.Is(err, service.ErrInvalidAttributeRequest) {
			writeError(w, http.StatusBadRequest, "invalid heatmap request", err.Error())
			return
		}
		writeFilterQueryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
