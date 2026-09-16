package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/rollup"
)

// metricsQueryHandlers holds session-authenticated metrics query handlers.
type metricsQueryHandlers struct {
	repo *repository.Repository
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

	names, err := h.repo.ListMetricNames(r.Context(), projectID, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
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

	entries, err := h.repo.ListMetricCatalog(r.Context(), projectID, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, metricCatalogResponse{Groups: groupCatalog(entries)})
}

// groupCatalog buckets entries by semantic prefix, preserving the alphabetical
// metric order within each group and ordering groups by first appearance.
func groupCatalog(entries []repository.MetricCatalogEntry) []catalogGroup {
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

// handleMetricSeries returns data points for a named metric.
//
// GET /api/v1/metrics/series?name=<name>&project_id=1&from=<rfc3339>&to=<rfc3339>&limit=1000&label[service.name]=foo
func (h *metricsQueryHandlers) handleMetricSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing name parameter", "")
		return
	}

	projectID := parseInt64Param(r, "project_id", 0)
	limit := parseIntParam(r, "limit", 1000)
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

	labels := parseLabelParams(r)
	for k := range labels {
		if !repository.ValidLabelKey(k) {
			writeError(w, http.StatusBadRequest, "invalid label key", "")
			return
		}
	}
	groupBy := parseGroupBy(r)

	// Long ranges read the downsampled rollups instead of scanning raw points.
	var (
		metricType, unit string
		in               []metrics.InputPoint
		step             int64
		queryErr         error
	)
	if to.Sub(from) > rollupQueryThreshold {
		metricType, unit, in, step, queryErr = h.rollupInput(r, projectID, name, from, to, labels, limit)
	} else {
		metricType, unit, in, queryErr = h.rawInput(r, projectID, name, from, to, labels, limit)
	}
	if queryErr != nil {
		writeError(w, http.StatusInternalServerError, "query failed", queryErr.Error())
		return
	}

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

// rollupQueryThreshold is the range width above which the series query reads
// pre-aggregated rollups rather than raw data points.
const rollupQueryThreshold = 6 * time.Hour

// rawInput loads raw metric data points and maps them to derivation input.
func (h *metricsQueryHandlers) rawInput(r *http.Request, projectID int64, name string, from, to time.Time, labels map[string]string, limit int) (string, string, []metrics.InputPoint, error) {
	rows, err := h.repo.QueryMetricSeries(r.Context(), repository.MetricFilter{
		ProjectID: projectID, Name: name, From: from, To: to, Attributes: labels, Limit: limit,
	})
	if err != nil {
		return "", "", nil, err
	}
	var metricType, unit string
	in := make([]metrics.InputPoint, 0, len(rows))
	for _, row := range rows {
		if metricType == "" {
			metricType, unit = row.Type, row.Unit
		}
		in = append(in, metrics.InputPoint{
			T:          row.TimeUnixNano,
			Value:      row.Value,
			Count:      row.Count,
			Extra:      repository.MarshalMetricExtra(row),
			Attributes: parseAttrs(row.Attributes),
		})
	}
	return metricType, unit, in, nil
}

// rollupTierSteps lists the resolutions worth trying for a range, finest first.
//
// The finest tier that still gives a readable chart is preferred, and the
// coarser ones are fallbacks: the fine tiers are kept for days while the coarse
// ones go back years, so a month-old week-long range simply has no 5-minute
// buckets left to read. Asking in order turns that into a slightly smoother
// chart instead of an empty one.
func rollupTierSteps(width time.Duration) []int64 {
	switch {
	case width <= 48*time.Hour:
		return []int64{rollup.Step5m, rollup.StepHour, rollup.StepDay}
	case width <= 30*24*time.Hour:
		return []int64{rollup.StepHour, rollup.StepDay, rollup.StepWeek}
	case width <= 365*24*time.Hour:
		return []int64{rollup.StepDay, rollup.StepWeek, rollup.StepMonth}
	default:
		return []int64{rollup.StepWeek, rollup.StepMonth}
	}
}

// rollupInput loads downsampled rollup buckets and maps them to derivation
// input, returning the resolution it read. The per-type value choice keeps
// metrics.Derive working unchanged: gauges use the bucket average, counters use
// the bucket-end cumulative value (so rate is derived across buckets),
// distributions carry their merged extra.
func (h *metricsQueryHandlers) rollupInput(r *http.Request, projectID int64, name string, from, to time.Time, labels map[string]string, limit int) (string, string, []metrics.InputPoint, int64, error) {
	filter := repository.MetricRollupFilter{
		ProjectID: projectID, Name: name, From: from, To: to, Attributes: labels, Limit: limit,
	}
	for _, step := range rollupTierSteps(to.Sub(from)) {
		rows, err := h.queryTier(r, step, filter)
		if err != nil {
			return "", "", nil, 0, err
		}
		if len(rows) == 0 {
			continue
		}
		var metricType, unit string
		in := make([]metrics.InputPoint, 0, len(rows))
		for _, row := range rows {
			if metricType == "" {
				metricType, unit = row.Type, row.Unit
			}
			in = append(in, rollupToInput(row))
		}
		return metricType, unit, in, step, nil
	}
	return "", "", nil, 0, nil
}

// queryTier reads one resolution. The 5-minute tier is the accumulator's own
// table; every coarser tier shares metric_rollups_coarse.
func (h *metricsQueryHandlers) queryTier(r *http.Request, step int64, f repository.MetricRollupFilter) ([]repository.MetricRollup, error) {
	if step == repository.Rollup5mStep {
		return h.repo.QueryMetricRollups(r.Context(), f)
	}
	return h.repo.QueryCoarseRollups(r.Context(), repository.CoarseRollupFilter{
		ProjectID:   f.ProjectID,
		Name:        f.Name,
		StepSeconds: step,
		From:        f.From,
		To:          f.To,
		Attributes:  f.Attributes,
		Limit:       f.Limit,
	})
}

// rollupToInput maps a rollup bucket to a derivation input point. The per-type
// value choice keeps metrics.Derive working unchanged: gauges use the bucket
// average, counters use the bucket-end cumulative value (so rate is derived
// across buckets), distributions carry their merged extra.
func rollupToInput(row repository.MetricRollup) metrics.InputPoint {
	value := row.Last
	if row.Type == "gauge" && row.Count > 0 {
		value = row.Sum / float64(row.Count)
	}
	return metrics.InputPoint{
		T:          row.Bucket.UnixNano(),
		Value:      value,
		Count:      row.ObsCount,
		Extra:      json.RawMessage(row.Extra),
		Attributes: parseAttrs(row.Attributes),
	}
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

// parseAttrs decodes a stored attributes JSON object into string labels.
func parseAttrs(raw string) map[string]string {
	return metrics.ParseAttributes([]byte(raw))
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
