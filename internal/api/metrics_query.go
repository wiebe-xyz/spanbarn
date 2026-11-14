package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// metricsQueryHandlers holds session-authenticated metrics query handlers.
type metricsQueryHandlers struct {
	svc *service.MetricsService
}

type metricNamesResponse struct {
	Names []string `json:"names"`
}

// metricSeriesResponse is the shape-aware series response. Raw OTLP points are
// split into one series per label set and transformed per metric type (rate for
// cumulative sums, p50/p95/p99 for distributions). Render tells the frontend
// how to draw them; see metrics.RenderFor.
type metricSeriesResponse struct {
	Name   string           `json:"name"`
	Type   string           `json:"type"`
	Unit   string           `json:"unit"`
	Render string           `json:"render"`
	Series []metrics.Series `json:"series"`
	// Step is the resolution the answer was served at, in seconds: 0 for raw
	// data points, otherwise the rollup tier's bucket width. A long range is
	// answered from coarser buckets, and the chart says so rather than implying
	// a precision it does not have.
	Step int64 `json:"step_seconds"`
}

// handleMetricNames returns distinct metric names for a project within a time range.
//
// GET /api/v1/metrics/names?project_id=1&from=<rfc3339>&to=<rfc3339>
func (h *metricsQueryHandlers) handleMetricNames(w http.ResponseWriter, r *http.Request) {
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

	names, err := h.svc.Names(r.Context(), projectID, from, to)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, metricNamesResponse{Names: names})
}

type catalogMetric struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Unit   string `json:"unit"`
	Series int64  `json:"series"`
}

type catalogGroup struct {
	Name    string          `json:"name"`
	Metrics []catalogMetric `json:"metrics"`
}

type metricCatalogResponse struct {
	Groups []catalogGroup `json:"groups"`
}

// handleMetricCatalog returns metric names grouped by their semantic prefix
// (the segment before the first '.', e.g. http, db, system), each with type,
// unit, and series count, so the UI can present organised, collapsible groups
// instead of one flat list.
//
// GET /api/v1/metrics/catalog?project_id=1&from=<rfc3339>&to=<rfc3339>
func (h *metricsQueryHandlers) handleMetricCatalog(w http.ResponseWriter, r *http.Request) {
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

	entries, err := h.svc.Catalog(r.Context(), projectID, from, to)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}

	writeJSON(w, http.StatusOK, metricCatalogResponse{Groups: groupCatalog(entries)})
}

// groupCatalog buckets entries by semantic prefix, preserving the alphabetical
// metric order within each group and ordering groups by first appearance.
func groupCatalog(entries []service.MetricCatalogEntry) []catalogGroup {
	order := []string{}
	byPrefix := map[string]*catalogGroup{}
	for _, e := range entries {
		prefix := "other"
		if i := strings.IndexByte(e.Name, '.'); i > 0 {
			prefix = e.Name[:i]
		}
		g := byPrefix[prefix]
		if g == nil {
			g = &catalogGroup{Name: prefix}
			byPrefix[prefix] = g
			order = append(order, prefix)
		}
		g.Metrics = append(g.Metrics, catalogMetric{Name: e.Name, Type: e.Type, Unit: e.Unit, Series: e.Series})
	}
	groups := make([]catalogGroup, 0, len(order))
	for _, p := range order {
		groups = append(groups, *byPrefix[p])
	}
	return groups
}

// metricQuery is a parsed, executed metric query: the input points of one
// named metric over a range plus the resolution they were read at.
type metricQuery struct {
	name, metricType, unit string
	in                     []metrics.InputPoint
	step                   int64
	groupBy                []string
}

// loadMetricQuery parses name, project_id, the time range, label filters and
// group_by, then reads raw points or rollups depending on the range. On a bad
// request or a failed query it writes the error response and returns false.
func (h *metricsQueryHandlers) loadMetricQuery(w http.ResponseWriter, r *http.Request, defaultLimit int) (metricQuery, bool) {
	req, ok := parseMetricRequest(w, r, defaultLimit)
	if !ok {
		return metricQuery{}, false
	}
	q, err := h.readMetricQuery(r.Context(), req, req.from, req.to)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return metricQuery{}, false
	}
	return q, true
}

// metricRequest is a parsed metric read: what to read, before any data is.
type metricRequest struct {
	name      string
	projectID int64
	limit     int
	from, to  time.Time
	labels    map[string]string
	groupBy   []string
	// totals marks a sum over the range, which reads the hour tier from a day on.
	totals bool
}

// cacheKey names every input of the read except the range.
func (m metricRequest) cacheKey(kind string) string {
	keys := make([]string, 0, len(m.labels))
	for k := range m.labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&lb, "%q=%q,", k, m.labels[k])
	}
	return fmt.Sprintf("metric:%s:%d:%q:%d:%s:%s", kind, m.projectID, m.name, m.limit, lb.String(), strings.Join(m.groupBy, ","))
}

// parseMetricRequest reads and checks the query parameters, writing the error
// response itself when they are wrong.
func parseMetricRequest(w http.ResponseWriter, r *http.Request, defaultLimit int) (metricRequest, bool) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing name parameter", "")
		return metricRequest{}, false
	}

	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return metricRequest{}, false
	}
	if from.IsZero() {
		from = time.Now().Add(-24 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}

	labels := parseLabelParams(r)
	for k := range labels {
		if !service.ValidLabelKey(k) {
			writeError(w, http.StatusBadRequest, "invalid label key", "")
			return metricRequest{}, false
		}
	}
	return metricRequest{
		name:      name,
		projectID: parseInt64Param(r, "project_id", 0),
		limit:     parseIntParam(r, "limit", defaultLimit),
		from:      from,
		to:        to,
		labels:    labels,
		groupBy:   parseGroupBy(r),
	}, true
}

// readMetricQuery reads the data of req over [from, to].
func (h *metricsQueryHandlers) readMetricQuery(ctx context.Context, req metricRequest, from, to time.Time) (metricQuery, error) {
	in, err := h.svc.ReadInput(ctx, service.MetricInputRequest{
		ProjectID: req.projectID, Name: req.name, From: from, To: to,
		Labels: req.labels, Limit: req.limit, Totals: req.totals,
	})
	if err != nil {
		return metricQuery{}, err
	}
	return metricQuery{name: req.name, metricType: in.Type, unit: in.Unit, in: in.Points, step: in.Step, groupBy: req.groupBy}, nil
}

// handleMetricSeries returns data points for a named metric.
//
// GET /api/v1/metrics/series?name=<name>&project_id=1&from=<rfc3339>&to=<rfc3339>&limit=1000&label[service.name]=foo
func (h *metricsQueryHandlers) handleMetricSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	q, ok := h.loadMetricQuery(w, r, 1000)
	if !ok {
		return
	}
	name, metricType, unit, step, groupBy := q.name, q.metricType, q.unit, q.step, q.groupBy
	in := q.in

	series := metrics.BuildSeries(metricType, in, groupBy)
	if series == nil {
		series = []metrics.Series{}
	}

	writeJSON(w, http.StatusOK, metricSeriesResponse{
		Name:   name,
		Type:   metricType,
		Unit:   unit,
		Render: string(metrics.RenderFor(metricType)),
		Series: series,
		Step:   step,
	})
}

// parseGroupBy reads the comma-separated group_by attribute keys. When empty,
// series are split by their full attribute set.
func parseGroupBy(r *http.Request) []string {
	raw := r.URL.Query().Get("group_by")
	if raw == "" {
		return nil
	}
	var keys []string
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// parseLabelParams extracts label[key]=value query parameters.
// The URL query parameter format is: label[service.name]=foo
func parseLabelParams(r *http.Request) map[string]string {
	labels := map[string]string{}
	for key, vals := range r.URL.Query() {
		if strings.HasPrefix(key, "label[") && strings.HasSuffix(key, "]") && len(vals) > 0 {
			labelKey := key[len("label[") : len(key)-1]
			labels[labelKey] = vals[0]
		}
	}
	return labels
}
