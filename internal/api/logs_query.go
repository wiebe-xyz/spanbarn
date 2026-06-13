package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type logsQueryHandlers struct {
	repo *repository.Repository
}

type logEntryJSON struct {
	ID             int64           `json:"id"`
	TraceID        string          `json:"traceId"`
	SpanID         string          `json:"spanId"`
	SeverityNumber int32           `json:"severityNumber"`
	SeverityText   string          `json:"severityText"`
	TimeUnixNano   int64           `json:"timeUnixNano"`
	Body           string          `json:"body"`
	Attributes     json.RawMessage `json:"attributes"`
	IngestedAt     time.Time       `json:"ingestedAt"`
}

type logsResponse struct {
	Logs  []logEntryJSON `json:"logs"`
	Total int            `json:"total"`
}

type pinnedTraceJSON struct {
	TraceID  string    `json:"traceId"`
	Label    string    `json:"label"`
	PinnedAt time.Time `json:"pinnedAt"`
}

type pinnedTracesResponse struct {
	Pinned []pinnedTraceJSON `json:"pinned"`
}

// handleLogs handles GET /api/v1/logs
func (h *logsQueryHandlers) handleLogs(w http.ResponseWriter, r *http.Request) {
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

	f := repository.LogFilter{
		ProjectID:   projectID,
		TraceID:     r.URL.Query().Get("trace_id"),
		SpanID:      r.URL.Query().Get("span_id"),
		MinSeverity: int32(parseIntParam(r, "severity", 0)),
		Service:     r.URL.Query().Get("service"),
		Search:      r.URL.Query().Get("search"),
		From:        from,
		To:          to,
		Limit:       parseIntParam(r, "limit", 200),
		Offset:      parseIntParam(r, "offset", 0),
	}

	rows, total, err := h.repo.QueryLogs(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}

	entries := make([]logEntryJSON, 0, len(rows))
	for _, row := range rows {
		attrs := json.RawMessage(row.Attributes)
		if len(attrs) == 0 {
			attrs = json.RawMessage("{}")
		}
		entries = append(entries, logEntryJSON{
			ID:             row.ID,
			TraceID:        row.TraceID,
			SpanID:         row.SpanID,
			SeverityNumber: row.SeverityNumber,
			SeverityText:   row.SeverityText,
			TimeUnixNano:   row.TimeUnixNano,
			Body:           row.Body,
			Attributes:     attrs,
			IngestedAt:     row.IngestedAt,
		})
	}

	writeJSON(w, http.StatusOK, logsResponse{Logs: entries, Total: total})
}

type logHistogramBucketJSON struct {
	Ts    string `json:"ts"`
	Count int    `json:"count"`
}

type logsHistogramResponse struct {
	Buckets []logHistogramBucketJSON `json:"buckets"`
}

// handleLogsHistogram handles GET /api/v1/logs/histogram
func (h *logsQueryHandlers) handleLogsHistogram(w http.ResponseWriter, r *http.Request) {
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

	span := to.Sub(from)
	var bucketSecs int
	switch {
	case span <= time.Hour:
		bucketSecs = 60
	case span <= 4*time.Hour:
		bucketSecs = 300
	case span <= 24*time.Hour:
		bucketSecs = 1800
	case span <= 7*24*time.Hour:
		bucketSecs = 10800
	default:
		bucketSecs = 86400
	}

	f := repository.LogFilter{
		ProjectID:   projectID,
		MinSeverity: int32(parseIntParam(r, "severity", 0)),
		Service:     r.URL.Query().Get("service"),
		Search:      r.URL.Query().Get("search"),
		From:        from,
		To:          to,
	}

	buckets, err := h.repo.LogHistogram(r.Context(), f, bucketSecs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "histogram query failed", err.Error())
		return
	}

	out := make([]logHistogramBucketJSON, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, logHistogramBucketJSON{
			Ts:    b.Ts.Format(time.RFC3339),
			Count: b.Count,
		})
	}
	writeJSON(w, http.StatusOK, logsHistogramResponse{Buckets: out})
}

// handlePinnedTraces handles GET/POST/DELETE on /api/v1/pinned-traces
func (h *logsQueryHandlers) handlePinnedTraces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listPinnedTraces(w, r)
	case http.MethodPost:
		h.pinTrace(w, r)
	case http.MethodDelete:
		h.unpinTrace(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
	}
}

func (h *logsQueryHandlers) listPinnedTraces(w http.ResponseWriter, r *http.Request) {
	projectID := parseInt64Param(r, "project_id", 0)
	pins, err := h.repo.ListPinnedTraces(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}
	out := make([]pinnedTraceJSON, 0, len(pins))
	for _, p := range pins {
		out = append(out, pinnedTraceJSON{TraceID: p.TraceID, Label: p.Label, PinnedAt: p.PinnedAt})
	}
	writeJSON(w, http.StatusOK, pinnedTracesResponse{Pinned: out})
}

func (h *logsQueryHandlers) pinTrace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID int64  `json:"project_id"`
		TraceID   string `json:"trace_id"`
		Label     string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
		return
	}
	if body.TraceID == "" {
		writeError(w, http.StatusBadRequest, "trace_id required", "")
		return
	}
	if err := h.repo.PinTrace(r.Context(), body.ProjectID, body.TraceID, body.Label); err != nil {
		writeError(w, http.StatusInternalServerError, "pin failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *logsQueryHandlers) unpinTrace(w http.ResponseWriter, r *http.Request) {
	// DELETE /api/v1/pinned-traces/{traceId}  or  ?trace_id=…&project_id=…
	projectID := parseInt64Param(r, "project_id", 0)

	// Extract traceId from URL path suffix.
	traceID := r.URL.Query().Get("trace_id")
	if traceID == "" {
		// Try path: /api/v1/pinned-traces/<traceId>
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/pinned-traces/")
		if path != "" && path != r.URL.Path {
			traceID = path
		}
	}
	if traceID == "" {
		writeError(w, http.StatusBadRequest, "trace_id required", "")
		return
	}
	if err := h.repo.UnpinTrace(r.Context(), projectID, traceID); err != nil {
		writeError(w, http.StatusInternalServerError, "unpin failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
