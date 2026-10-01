package api

import (
	"errors"
	"net/http"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// handleAttributes lists the attribute keys of a project's spans in a required
// time range, with coverage, cardinality and top values.
func (h *queryHandlers) handleAttributes(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.attributes")
	defer span.End()

	from, to, ok := parseGetTimeRange(w, r)
	if !ok {
		return
	}
	q := service.AttributeQuery{
		ProjectID: parseInt64Param(r, "project_id", 0),
		From:      from,
		To:        to,
		SpanName:  r.URL.Query().Get("span_name"),
		Service:   r.URL.Query().Get("service"),
		Key:       r.URL.Query().Get("key"),
		Sample:    parseIntParam(r, "sample", 0),
		MaxSpans:  parseIntParam(r, "max_spans", 0),
		Limit:     parseIntParam(r, "limit", 0),
		Top:       parseIntParam(r, "top", 0),
	}
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID))
	res, err := h.svc.DiscoverAttributes(ctx, q)
	if err != nil {
		if errors.Is(err, service.ErrInvalidAttributeRequest) {
			writeError(w, http.StatusBadRequest, "invalid attribute request", err.Error())
			return
		}
		writeServerError(w, r, "query failed", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
