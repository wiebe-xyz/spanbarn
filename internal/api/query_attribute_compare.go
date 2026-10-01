package api

import (
	"errors"
	"net/http"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// parseFilterNamed reads a filter model from the named query parameter. It
// writes the 400 itself and reports false on a bad filter.
func parseFilterNamed(w http.ResponseWriter, r *http.Request, name string) (*filter.Expr, bool) {
	expr, err := filter.Parse(r.URL.Query().Get(name))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid "+name+" filter", err.Error())
		return nil, false
	}
	return expr, true
}

// handleAttributeCompare ranks the attributes whose values differ most between
// a selection filter and a baseline filter.
func (h *queryHandlers) handleAttributeCompare(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.attribute_compare")
	defer span.End()

	from, to, ok := parseGetTimeRange(w, r)
	if !ok {
		return
	}
	selection, ok := parseFilterNamed(w, r, "selection")
	if !ok {
		return
	}
	baseline, ok := parseFilterNamed(w, r, "baseline")
	if !ok {
		return
	}
	q := service.AttributeCompareQuery{
		ProjectID: parseInt64Param(r, "project_id", 0),
		From:      from,
		To:        to,
		Selection: selection,
		Baseline:  baseline,
		Sample:    parseIntParam(r, "sample", 0),
		MaxSpans:  parseIntParam(r, "max_spans", 0),
		Limit:     parseIntParam(r, "limit", 0),
		Top:       parseIntParam(r, "top", 0),
	}
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID))
	res, err := h.svc.CompareAttributes(ctx, q)
	if err != nil {
		if errors.Is(err, service.ErrInvalidAttributeRequest) {
			writeError(w, http.StatusBadRequest, "invalid attribute comparison", err.Error())
			return
		}
		writeFilterQueryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
