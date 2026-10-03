package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestRouteGroupsRegisterOnlyWhenConfigured pins which route groups exist for a
// server built with only an ingest handler: public and ingest routes answer,
// query, management and project routes are absent.
func TestRouteGroupsRegisterOnlyWhenConfigured(t *testing.T) {
	ts := newTestServer(t)

	tests := []struct {
		method string
		path   string
		want   func(code int) bool
		desc   string
	}{
		{http.MethodGet, "/api/v1/health", func(c int) bool { return c == http.StatusOK }, "health"},
		{http.MethodPost, "/api/v1/spans", func(c int) bool { return c == http.StatusUnauthorized }, "ingest requires a key"},
		{http.MethodPost, "/v1/traces", func(c int) bool { return c == http.StatusUnauthorized }, "otlp requires a key"},
		{http.MethodPost, "/api/v1/telemetry", func(c int) bool { return c == http.StatusNotFound }, "telemetry needs sessions"},
		{http.MethodGet, "/api/v1/traces", func(c int) bool { return c == http.StatusNotFound }, "queries need a query service"},
		{http.MethodGet, "/api/v1/projects", func(c int) bool { return c == http.StatusNotFound }, "projects need a repository"},
		{http.MethodGet, "/api/v1/alerts", func(c int) bool { return c == http.StatusNotFound }, "alerts need a repository"},
		{http.MethodGet, "/api/v1/setup/demo", func(c int) bool { return c == http.StatusNotFound }, "setup needs a repository"},
	}
	for _, tc := range tests {
		req, err := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if !tc.want(resp.StatusCode) {
			t.Errorf("%s: %s %s returned %d", tc.desc, tc.method, tc.path, resp.StatusCode)
		}
	}
}
