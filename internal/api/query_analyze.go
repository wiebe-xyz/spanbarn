package api

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// parseAnalyzeRequest reads a group-by query from the URL: project_id, from and
// to (required), filter (JSON), group_by and calc (repeatable), order_by,
// order (asc or desc), limit, sample, max_spans and bucket. It writes the 400
// itself and reports false on a bad request.
func parseAnalyzeRequest(w http.ResponseWriter, r *http.Request) (service.AnalyzeRequest, bool) {
	from, to, ok := parseGetTimeRange(w, r)
	if !ok {
		return service.AnalyzeRequest{}, false
	}
	expr, ok := parseFilterParam(w, r)
	if !ok {
		return service.AnalyzeRequest{}, false
	}
	q := r.URL.Query()
	return service.AnalyzeRequest{
		ProjectID:     parseInt64Param(r, "project_id", 0),
		From:          from,
		To:            to,
		Expr:          expr,
		GroupBy:       q["group_by"],
		Calcs:         q["calc"],
		OrderBy:       q.Get("order_by"),
		Asc:           q.Get("order") == "asc",
		Limit:         parseIntParam(r, "limit", 0),
		Sample:        parseIntParam(r, "sample", 0),
		MaxSpans:      parseIntParam(r, "max_spans", 0),
		BucketSeconds: parseInt64Param(r, "bucket", 0),
	}, true
}

// handleAnalyze serves the group-by table: calculations per group in a required
// time range, with a group cap and an other row.
func (h *queryHandlers) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.analyze")
	defer span.End()

	req, ok := parseAnalyzeRequest(w, r)
	if !ok {
		return
	}
	span.SetAttributes(attribute.Int64("project_id", req.ProjectID))
	res, err := h.svc.Analyze(ctx, req)
	if err != nil {
		writeFilterQueryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleAnalyzeSeries serves one calculation over time for the largest groups.
func (h *queryHandlers) handleAnalyzeSeries(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.analyze_series")
	defer span.End()

	req, ok := parseAnalyzeRequest(w, r)
	if !ok {
		return
	}
	span.SetAttributes(attribute.Int64("project_id", req.ProjectID))
	res, err := h.svc.AnalyzeSeries(ctx, req)
	if err != nil {
		writeFilterQueryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
