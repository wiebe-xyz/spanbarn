package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// metricsQueryHandlers holds session-authenticated metrics query handlers.
type metricsQueryHandlers struct {
	repo *repository.Repository
}

type metricNamesResponse struct {
	Names []string `json:"names"`
}

type metricPoint struct {
	T          int64           `json:"t"`
	Value      float64         `json:"value"`
	Count      int64           `json:"count"`
	Attributes json.RawMessage `json:"attributes"`
	Extra      json.RawMessage `json:"extra,omitempty"`
}

type metricSeriesResponse struct {
	Name   string        `json:"name"`
	Type   string        `json:"type"`
	Unit   string        `json:"unit"`
	Points []metricPoint `json:"points"`
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

	rows, err := h.repo.QueryMetricSeries(r.Context(), repository.MetricFilter{
		ProjectID:  projectID,
		Name:       name,
		From:       from,
		To:         to,
		Attributes: labels,
		Limit:      limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}

	var metricType, unit string
	points := make([]metricPoint, 0, len(rows))
	for _, row := range rows {
		if metricType == "" {
			metricType = row.Type
			unit = row.Unit
		}
		points = append(points, metricPoint{
			T:          row.TimeUnixNano,
			Value:      row.Value,
			Count:      row.Count,
			Attributes: json.RawMessage(row.Attributes),
			Extra:      repository.MarshalMetricExtra(row),
		})
	}

	writeJSON(w, http.StatusOK, metricSeriesResponse{
		Name:   name,
		Type:   metricType,
		Unit:   unit,
		Points: points,
	})
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
