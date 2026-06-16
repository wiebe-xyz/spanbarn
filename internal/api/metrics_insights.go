package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type metricInsightsResponse struct {
	Insights []metrics.Insight `json:"insights"`
}

// maxInsights caps how many notable changes are returned.
const maxInsights = 20

// handleMetricInsights scans every metric series in the range (via rollups) and
// returns the most notable changes — spikes, drops, percentile regressions, and
// newly-appeared series — so users don't have to eyeball every graph.
//
// GET /api/v1/metrics/insights?project_id=1&from=<rfc3339>&to=<rfc3339>
func (h *metricsQueryHandlers) handleMetricInsights(w http.ResponseWriter, r *http.Request) {
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

	rows, err := h.repo.QueryProjectRollups(r.Context(), projectID, from, to, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}

	// Recent window = last 30% of the range; the rest is the baseline.
	splitNano := from.Add(time.Duration(float64(to.Sub(from)) * 0.7)).UnixNano()

	insights := detectInsights(rows, splitNano)
	sort.SliceStable(insights, func(i, j int) bool {
		return insights[i].Magnitude() > insights[j].Magnitude()
	})
	if len(insights) > maxInsights {
		insights = insights[:maxInsights]
	}
	if insights == nil {
		insights = []metrics.Insight{}
	}

	writeJSON(w, http.StatusOK, metricInsightsResponse{Insights: insights})
}

// detectInsights groups rollup rows into series (by name + fingerprint) and runs
// change detection on each. Rows are already ordered by name, fingerprint and
// bucket, so a series is a contiguous run.
func detectInsights(rows []repository.MetricRollup, splitNano int64) []metrics.Insight {
	var out []metrics.Insight

	flush := func(name, typ string, attrs string, pts []metrics.InputPoint) {
		if len(pts) == 0 {
			return
		}
		if in, ok := metrics.DetectSeries(name, typ, metrics.ParseAttributes([]byte(attrs)), pts, splitNano); ok {
			out = append(out, in)
		}
	}

	var (
		curName, curType, curAttrs string
		curFP                      string
		pts                        []metrics.InputPoint
	)
	for _, row := range rows {
		if row.Name != curName || row.AttrFingerprint != curFP {
			flush(curName, curType, curAttrs, pts)
			curName, curType, curAttrs, curFP = row.Name, row.Type, row.Attributes, row.AttrFingerprint
			pts = pts[:0]
		}
		pts = append(pts, rollupToInput(row))
	}
	flush(curName, curType, curAttrs, pts)
	return out
}
