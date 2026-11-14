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
	"github.com/wiebe-xyz/spanbarn/internal/tier"
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
	metricType, unit, in, step, err := h.readMetricInput(ctx, req.projectID, req.name, from, to, req.labels, req.limit, req.totals)
	if err != nil {
		return metricQuery{}, err
	}
	return metricQuery{name: req.name, metricType: metricType, unit: unit, in: in, step: step, groupBy: req.groupBy}, nil
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

// rollupQueryThreshold is the range width above which the series query reads
// pre-aggregated rollups rather than raw data points.
const rollupQueryThreshold = 6 * time.Hour

// filteredFineTierWidth is the widest range a label filter reads from the
// 5-minute tier. The filter has to look at every row in the range unless enough
// of them match to reach the limit, so a selective filter costs far more than an
// unfiltered read, and a two-day read of one times out where the hour tier answers
// in under a second.
const filteredFineTierWidth = 24 * time.Hour

// readMetricInput reads raw points for a narrow range and rollups for a long one,
// which spares a long range the raw scan. A narrow range with no raw points also
// reads rollups: raw points expire long before the tiers do, and an empty result
// there would look like a metric that reported nothing. The returned step is 0
// for raw points.
func (h *metricsQueryHandlers) readMetricInput(ctx context.Context, projectID int64, name string, from, to time.Time, labels map[string]string, limit int, totals bool) (string, string, []metrics.InputPoint, int64, error) {
	if to.Sub(from) > rollupQueryThreshold {
		return h.rollupInput(ctx, projectID, name, from, to, labels, limit, totals)
	}
	metricType, unit, in, err := h.rawInput(ctx, projectID, name, from, to, labels, limit)
	if err != nil || len(in) > 0 {
		return metricType, unit, in, 0, err
	}
	return h.rollupInput(ctx, projectID, name, from, to, labels, limit, totals)
}

// rawInput loads raw metric data points and maps them to derivation input.
func (h *metricsQueryHandlers) rawInput(ctx context.Context, projectID int64, name string, from, to time.Time, labels map[string]string, limit int) (string, string, []metrics.InputPoint, error) {
	rows, err := h.svc.Series(ctx, service.MetricFilter{
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
		p := metrics.InputPoint{
			T:          row.TimeUnixNano,
			Value:      row.Value,
			Count:      row.Count,
			Extra:      service.MarshalMetricExtra(row),
			Attributes: parseAttrs(row.Attributes),
		}
		if row.DeltaSum() {
			p.Delta = true
			p.Extra = nil
			if row.StartTimeUnixNano > 0 && row.TimeUnixNano > row.StartTimeUnixNano {
				p.IntervalNano = row.TimeUnixNano - row.StartTimeUnixNano
			}
		}
		in = append(in, p)
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
		return []int64{tier.Step5m, tier.StepHour, tier.StepDay}
	case width <= 30*24*time.Hour:
		return []int64{tier.StepHour, tier.StepDay, tier.StepWeek}
	case width <= 365*24*time.Hour:
		return []int64{tier.StepDay, tier.StepWeek, tier.StepMonth}
	default:
		return []int64{tier.StepWeek, tier.StepMonth}
	}
}

// tierStepsForRead is rollupTierSteps with the 5-minute tier left out of a
// filtered read too wide for it. Falling back to the whole range of the 5-minute
// tier would turn every filter that matches nothing into a timeout, so the hour
// tier read takes only its uncompacted tail from there (hourTierWithTail).
//
// A totals read starts at the hour tier from a day on, filtered or not. It sums
// every bucket in the range and has no chart to keep smooth, so the 12 times
// fewer rows are all gain: a delta total is the same sum either way, and a
// cumulative one only loses a reset that happened inside one hour.
func tierStepsForRead(width time.Duration, labels map[string]string, totals bool) []int64 {
	steps := rollupTierSteps(width)
	wide := len(labels) > 0 && width > filteredFineTierWidth
	if totals && width >= filteredFineTierWidth {
		wide = true
	}
	if wide && steps[0] == tier.Step5m {
		return steps[1:]
	}
	return steps
}

// rollupInput loads downsampled rollup buckets and maps them to derivation
// input, returning the resolution it read. The per-type value choice keeps
// metrics.Derive working unchanged: gauges use the bucket average, counters use
// the bucket-end cumulative value (so rate is derived across buckets),
// distributions carry their merged extra. Each read starts at the bucket holding
// from, so a range narrower than a coarse bucket still finds that bucket.
func (h *metricsQueryHandlers) rollupInput(ctx context.Context, projectID int64, name string, from, to time.Time, labels map[string]string, limit int, totals bool) (string, string, []metrics.InputPoint, int64, error) {
	filter := service.MetricRollupFilter{
		ProjectID: projectID, Name: name, From: from, To: to, Attributes: labels, Limit: limit,
	}
	fineRead := false
	for _, step := range tierStepsForRead(to.Sub(from), labels, totals) {
		filter.From = tier.TruncateBucket(from, step)
		var rows []service.MetricRollup
		var err error
		// A read that has tried the 5-minute tier and found nothing has no
		// tail to add.
		if step == tier.StepHour && !fineRead {
			rows, err = h.hourTierWithTail(ctx, filter, from)
		} else {
			rows, err = h.queryTier(ctx, step, filter)
		}
		fineRead = fineRead || step == tier.Step5m
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
			p := rollupToInput(row)
			if p.Delta {
				width := row.StepSeconds
				if width == 0 {
					width = step
				}
				p.IntervalNano = width * int64(time.Second)
			}
			in = append(in, p)
		}
		return metricType, unit, in, step, nil
	}
	return "", "", nil, 0, nil
}

// queryTier reads one resolution. The 5-minute tier is the accumulator's own
// table; every coarser tier shares metric_rollups_coarse.
func (h *metricsQueryHandlers) queryTier(ctx context.Context, step int64, f service.MetricRollupFilter) ([]service.MetricRollup, error) {
	if step == service.Rollup5mStep {
		return h.svc.Rollups(ctx, f)
	}
	return h.svc.CoarseRollups(ctx, service.CoarseRollupFilter{
		ProjectID:   f.ProjectID,
		Name:        f.Name,
		StepSeconds: step,
		From:        f.From,
		To:          f.To,
		Attributes:  f.Attributes,
		Limit:       f.Limit,
	})
}

// rollupToInput maps a rollup bucket to a derivation input point with its
// attributes, which the series path groups by.
func rollupToInput(row service.MetricRollup) metrics.InputPoint {
	p := service.RollupInputPoint(row)
	p.Attributes = parseAttrs(row.Attributes)
	return p
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
