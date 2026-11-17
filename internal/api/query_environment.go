package api

import (
	"context"
	"net/http"
	"sort"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// environmentParam names the query parameter every filter-aware read accepts:
// an environment name, or "all". Absent means no environment filter, so
// existing callers read what they always read; the web app sends production
// by default (#558).
const environmentParam = "environment"

// withEnvironment conjoins the request's environment condition onto expr. It
// writes the 400 itself and reports false on a bad name or a filter that
// cannot take the extra condition without nesting groups two levels deep.
func withEnvironment(w http.ResponseWriter, r *http.Request, expr *filter.Expr) (*filter.Expr, bool) {
	env := r.URL.Query().Get(environmentParam)
	node, ok := filter.EnvironmentNode(env)
	if !ok {
		return expr, true
	}
	if !validEnvironment(w, env) {
		return nil, false
	}
	out, ok := filter.Conjoin(expr, []filter.Node{node})
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid filter", "the environment condition cannot be added to a filter with nested groups under or")
		return nil, false
	}
	return out, true
}

// validEnvironment writes the 400 for a malformed environment name and reports
// whether the name may be used.
func validEnvironment(w http.ResponseWriter, env string) bool {
	if filter.ValidEnvironmentName(env) {
		return true
	}
	writeError(w, http.StatusBadRequest, "invalid environment", "use a lowercase name such as production, staging or testing, or all")
	return false
}

// scopeEnvironment puts the request's environment onto ctx for the reads that
// serve from aggregate rows, which carry the environment as a column and not as
// a span filter. Absent and "all" leave ctx unscoped. It writes the 400 itself
// and reports false on a bad name.
func scopeEnvironment(ctx context.Context, w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	env := r.URL.Query().Get(environmentParam)
	if env == "" || env == "all" {
		return ctx, true
	}
	if !validEnvironment(w, env) {
		return ctx, false
	}
	return filter.WithEnvironmentScope(ctx, env), true
}

// environmentRow is one environment of a project and its span count in the
// requested range. A span without the attribute counts under production.
type environmentRow struct {
	Environment string `json:"environment"`
	Spans       int64  `json:"spans"`
}

// handleEnvironments lists the environments a project holds in a required
// time range, largest first.
func (h *queryHandlers) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	ctx, span := apiTracer.Start(r.Context(), "api.query.environments")
	defer span.End()

	from, to, ok := parseGetTimeRange(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Analyze(ctx, service.AnalyzeRequest{
		ProjectID: parseInt64Param(r, "project_id", 0),
		From:      from,
		To:        to,
		GroupBy:   []string{filter.EnvironmentKey},
		Calcs:     []string{"count"},
	})
	if err != nil {
		writeFilterQueryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, environmentRows(res))
}

// environmentRows folds the group-by rows into one row per environment,
// mapping the empty group to production, largest first.
func environmentRows(res *service.AnalyzeResponse) []environmentRow {
	byName := map[string]int64{}
	add := func(row service.AnalyzeRow) {
		name := filter.ProductionEnvironment
		if len(row.Group) > 0 && row.Group[0] != "" {
			name = row.Group[0]
		}
		byName[name] += row.Count
	}
	for _, row := range res.Rows {
		add(row)
	}
	if res.Other != nil {
		add(*res.Other)
	}
	out := make([]environmentRow, 0, len(byName))
	for name, n := range byName {
		out = append(out, environmentRow{Environment: name, Spans: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Spans != out[j].Spans {
			return out[i].Spans > out[j].Spans
		}
		return out[i].Environment < out[j].Environment
	})
	return out
}
