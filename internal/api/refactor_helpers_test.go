package api

import (
	"net/http"
	"testing"
)

func TestNormalizeCreateAlertRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     alertRequest
		wantMsg string
	}{
		{"missing project", alertRequest{Type: "latency"}, "projectId and type are required"},
		{"bad type", alertRequest{ProjectID: 1, Type: "nope"}, "type must be 'latency', 'error_rate', or 'metric_threshold'"},
		{"metric without name", alertRequest{ProjectID: 1, Type: "metric_threshold"}, "metricName is required for metric_threshold alerts"},
		{"metric bad agg", alertRequest{ProjectID: 1, Type: "metric_threshold", MetricName: "m", MetricAgg: "max"}, "metricAgg must be one of rate, avg, p95, last"},
		{"latency without service", alertRequest{ProjectID: 1, Type: "latency"}, "service is required for this alert type"},
		{"valid latency", alertRequest{ProjectID: 1, Type: "latency", Service: "s"}, ""},
		{"valid metric", alertRequest{ProjectID: 1, Type: "metric_threshold", MetricName: "m"}, ""},
	}
	for _, tc := range tests {
		req := tc.req
		if got := normalizeCreateAlertRequest(&req); got != tc.wantMsg {
			t.Errorf("%s: message = %q, want %q", tc.name, got, tc.wantMsg)
		}
	}

	req := alertRequest{ProjectID: 1, Type: "metric_threshold", MetricName: "m"}
	normalizeCreateAlertRequest(&req)
	if req.MetricAgg != "last" || req.ComparisonWindow != 10 || req.CooldownMinutes != 30 {
		t.Errorf("defaults not applied: %+v", req)
	}
}

func TestProjectIDRoutes(t *testing.T) {
	h := &projectHandlers{}
	tests := []struct {
		sub     string
		methods []string
		known   bool
	}{
		{"", []string{http.MethodDelete}, true},
		{"approve", []string{http.MethodPost}, true},
		{"apikeys", []string{http.MethodGet}, true},
		{"e2e", []string{http.MethodPost, http.MethodDelete}, true},
		{"verbose", []string{http.MethodPost, http.MethodDelete}, true},
		{"unknown", nil, false},
		{"approve/extra", nil, false},
	}
	for _, tc := range tests {
		routes := h.idRoutes(tc.sub)
		if (routes != nil) != tc.known {
			t.Errorf("sub %q known = %v, want %v", tc.sub, routes != nil, tc.known)
			continue
		}
		if len(routes) != len(tc.methods) {
			t.Errorf("sub %q has %d methods, want %d", tc.sub, len(routes), len(tc.methods))
		}
		for _, m := range tc.methods {
			if routes[m] == nil {
				t.Errorf("sub %q missing method %s", tc.sub, m)
			}
		}
	}
}

func TestQueryResolveRoute(t *testing.T) {
	h := &queryHandlers{}
	tests := []struct {
		path  []string
		found bool
	}{
		{[]string{"api", "v1", "services"}, true},
		{[]string{"api", "v1", "services", "a", "operations"}, true},
		{[]string{"api", "v1", "services", "a", "operations", "op", "timeseries"}, true},
		{[]string{"api", "v1", "services", "a", "other"}, false},
		{[]string{"api", "v1", "traces"}, true},
		{[]string{"api", "v1", "traces", "groups"}, true},
		{[]string{"api", "v1", "traces", "abc"}, true},
		{[]string{"api", "v1", "traces", "abc", "x"}, false},
		{[]string{"api", "v1", "dependencies"}, true},
		{[]string{"api", "v1", "dependencies", "traces"}, true},
		{[]string{"api", "v1", "dependencies", "x"}, false},
		{[]string{"api", "v1", "database"}, true},
		{[]string{"api", "v1", "database", "x"}, false},
		{[]string{"api", "v1", "prompts"}, true},
		{[]string{"api", "v1", "prompts", "detail"}, true},
		{[]string{"api", "v1", "prompts", "x"}, false},
		{[]string{"api", "v1", "unknown"}, false},
	}
	for _, tc := range tests {
		if got := h.resolveRoute(tc.path) != nil; got != tc.found {
			t.Errorf("%v found = %v, want %v", tc.path, got, tc.found)
		}
	}
}
