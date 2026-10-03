package api

import (
	"net/http"
	"strings"
)

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
	// Use RawPath to preserve %2F in operation names; fall back to Path
	path := r.URL.RawPath
	if path == "" {
		path = r.URL.Path
	}
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	// parts[0]=api, parts[1]=v1, parts[2]=resource...
	if len(parts) >= 3 {
		if handler := h.resolveRoute(parts); handler != nil {
			handler(w, r)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not found", "")
}

// resolveRoute returns the handler for the path parts, or nil when no route
// matches.
func (h *queryHandlers) resolveRoute(parts []string) http.HandlerFunc {
	switch parts[2] {
	case "services":
		return h.resolveServiceRoute(parts)
	case "traces":
		return h.resolveTraceRoute(parts)
	case "dependencies":
		return h.resolveDependencyRoute(parts)
	case "database":
		if len(parts) == 3 {
			return h.handleDatabaseQueries
		}
	case "prompts":
		return h.resolvePromptRoute(parts)
	}
	return nil
}

func (h *queryHandlers) resolveServiceRoute(parts []string) http.HandlerFunc {
	switch {
	case len(parts) == 3:
		// GET /api/v1/services
		return h.handleServices
	case len(parts) == 5 && parts[4] == "operations":
		// GET /api/v1/services/{service}/operations
		return h.handleOperations
	case len(parts) >= 7 && parts[4] == "operations" && parts[len(parts)-1] == "timeseries":
		// GET /api/v1/services/{service}/operations/{operation}/timeseries
		return h.handleTimeseries
	}
	return nil
}

func (h *queryHandlers) resolveTraceRoute(parts []string) http.HandlerFunc {
	switch {
	case len(parts) == 3:
		// GET /api/v1/traces
		return h.handleTraces
	case len(parts) == 4 && parts[3] == "groups":
		// GET /api/v1/traces/groups
		return h.handleTraceGroups
	case len(parts) == 4:
		// GET /api/v1/traces/{traceId}
		return h.handleTraceDetail
	}
	return nil
}

func (h *queryHandlers) resolveDependencyRoute(parts []string) http.HandlerFunc {
	switch {
	case len(parts) == 3:
		return h.handleDependencies
	case len(parts) == 4 && parts[3] == "traces":
		return h.handleDependencyTraces
	}
	return nil
}

func (h *queryHandlers) resolvePromptRoute(parts []string) http.HandlerFunc {
	switch {
	case len(parts) == 3:
		return h.handlePrompts
	case len(parts) == 4 && parts[3] == "detail":
		return h.handlePromptDetail
	}
	return nil
}
