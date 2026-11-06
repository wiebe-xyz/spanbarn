package selfmetrics

import "testing"

// TestLatencyExportedPerRoute: each route pattern gets its own delta histogram
// point labelled http.route, a request with no matched route counts under
// "unmatched", and a route with no requests in the next interval exports
// nothing.
func TestLatencyExportedPerRoute(t *testing.T) {
	rec := NewRecorder()
	rec.RecordRequest("/api/v1/prompts", "2xx", 3000)
	rec.RecordRequest("/api/v1/prompts", "2xx", 40)
	rec.RecordRequest("POST /v1/traces", "2xx", 2)
	rec.RecordRequest("", "4xx", 1)

	m := exportedMetrics(t, rec)[metricDuration]
	if m == nil {
		t.Fatal("latency not exported")
	}
	byRoute := map[string]uint64{}
	for _, dp := range m.GetHistogram().DataPoints {
		if len(dp.Attributes) != 1 || dp.Attributes[0].Key != attrRoute {
			t.Fatalf("point attributes = %v, want only %s", dp.Attributes, attrRoute)
		}
		byRoute[dp.Attributes[0].Value.GetStringValue()] = dp.Count
	}
	want := map[string]uint64{"/api/v1/prompts": 2, "POST /v1/traces": 1, unmatchedRoute: 1}
	if len(byRoute) != len(want) {
		t.Fatalf("routes = %v, want %v", byRoute, want)
	}
	for r, c := range want {
		if byRoute[r] != c {
			t.Errorf("route %q count = %d, want %d", r, byRoute[r], c)
		}
	}

	rec.RecordRequest("POST /v1/traces", "2xx", 2)
	next := exportedMetrics(t, rec)[metricDuration].GetHistogram().DataPoints
	if len(next) != 1 || next[0].Attributes[0].Value.GetStringValue() != "POST /v1/traces" {
		t.Errorf("next interval points = %v, want only the route that had requests", next)
	}
}
