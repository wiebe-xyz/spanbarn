package api

import (
	"errors"
	"net/http"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// parseFilterParam reads the shared filter model from the `filter` query
// parameter (JSON). It writes the 400 itself and reports false on a bad filter.
func parseFilterParam(w http.ResponseWriter, r *http.Request) (*filter.Expr, bool) {
	expr, err := filter.Parse(r.URL.Query().Get("filter"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid filter", err.Error())
		return nil, false
	}
	return expr, true
}

// writeFilterQueryError maps a filter validation error to 400 and anything else
// to 500.
func writeFilterQueryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, filter.ErrInvalid) {
		writeError(w, http.StatusBadRequest, "invalid filter", err.Error())
		return
	}
	writeServerError(w, r, "query failed", err)
}

// handleSpanSearch lists spans matching the filter model in a required range.
func (h *queryHandlers) handleSpanSearch(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.span_search")
	defer span.End()

	from, to, ok := parseGetTimeRange(w, r)
	if !ok {
		return
	}
	expr, ok := parseFilterParam(w, r)
	if !ok {
		return
	}
	q := service.SpanSearchFilter{
		ProjectID: parseInt64Param(r, "project_id", 0),
		Expr:      expr,
		From:      from,
		To:        to,
		Limit:     parseIntParam(r, "limit", 0),
		Offset:    parseIntParam(r, "offset", 0),
	}
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID))
	spans, err := h.svc.SearchSpans(ctx, q)
	if err != nil {
		writeFilterQueryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, spans)
}
