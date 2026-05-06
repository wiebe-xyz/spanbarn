package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// queryHandlers holds session-authenticated query endpoint handlers.
type queryHandlers struct {
	svc *service.QueryService
}

func (h *queryHandlers) handleServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return
	}

	projectID := parseInt64Param(r, "project_id", 0)

	services, err := h.svc.ListServices(r.Context(), projectID, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, services)
}

func (h *queryHandlers) handleOperations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	svcName := pathParam(r.URL.Path, "service")
	if svcName == "" {
		writeError(w, http.StatusBadRequest, "missing service", "")
		return
	}

	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return
	}

	ops, err := h.svc.ListOperations(r.Context(), 0, svcName, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ops)
}

func (h *queryHandlers) handleTimeseries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	svcName := pathParam(r.URL.Path, "service")
	opName := pathParam(r.URL.Path, "operation")
	if svcName == "" || opName == "" {
		writeError(w, http.StatusBadRequest, "missing service or operation", "")
		return
	}

	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return
	}

	interval := parseInterval(r.URL.Query().Get("interval"))

	ts, err := h.svc.GetTimeseries(r.Context(), 0, svcName, opName, from, to, interval)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ts)
}

func (h *queryHandlers) handleTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return
	}

	limit := parseIntParam(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}

	filter := service.TraceSearchFilter{
		Service:       r.URL.Query().Get("service"),
		Operation:     r.URL.Query().Get("operation"),
		Status:        r.URL.Query().Get("status"),
		MinDurationUs: parseInt64Param(r, "min_duration_us", 0),
		From:          from,
		To:            to,
		Limit:         limit,
		Offset:        parseIntParam(r, "offset", 0),
	}

	traces, err := h.svc.SearchTraces(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, traces)
}

func (h *queryHandlers) handleTraceDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	traceID := pathParam(r.URL.Path, "traceId")
	if traceID == "" {
		writeError(w, http.StatusBadRequest, "missing traceId", "")
		return
	}

	detail, err := h.svc.GetTrace(r.Context(), traceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}
	if detail == nil {
		writeError(w, http.StatusNotFound, "trace not found", "")
		return
	}

	writeJSON(w, http.StatusOK, detail)
}

func (h *queryHandlers) handleDependencies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return
	}

	svcFilter := r.URL.Query().Get("service")

	deps, err := h.svc.ListDependencies(r.Context(), 0, from, to, svcFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, deps)
}

// routeQuery is a handler that dispatches query routes based on URL path pattern.
// It handles the following patterns:
//
//	/api/v1/services
//	/api/v1/services/{service}/operations
//	/api/v1/services/{service}/operations/{operation}/timeseries
//	/api/v1/traces
//	/api/v1/traces/{traceId}
//	/api/v1/dependencies
func (h *queryHandlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	// parts[0]=api, parts[1]=v1, parts[2]=resource...
	if len(parts) < 3 {
		writeError(w, http.StatusNotFound, "not found", "")
		return
	}

	resource := parts[2]

	switch resource {
	case "services":
		switch {
		case len(parts) == 3:
			// GET /api/v1/services
			h.handleServices(w, r)
		case len(parts) == 5 && parts[4] == "operations":
			// GET /api/v1/services/{service}/operations
			h.handleOperations(w, r)
		case len(parts) == 7 && parts[4] == "operations" && parts[6] == "timeseries":
			// GET /api/v1/services/{service}/operations/{operation}/timeseries
			h.handleTimeseries(w, r)
		default:
			writeError(w, http.StatusNotFound, "not found", "")
		}
	case "traces":
		switch {
		case len(parts) == 3:
			// GET /api/v1/traces
			h.handleTraces(w, r)
		case len(parts) == 4:
			// GET /api/v1/traces/{traceId}
			h.handleTraceDetail(w, r)
		default:
			writeError(w, http.StatusNotFound, "not found", "")
		}
	case "dependencies":
		if len(parts) == 3 {
			h.handleDependencies(w, r)
		} else {
			writeError(w, http.StatusNotFound, "not found", "")
		}
	default:
		writeError(w, http.StatusNotFound, "not found", "")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
