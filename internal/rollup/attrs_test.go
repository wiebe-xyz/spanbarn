package rollup

import (
	"encoding/json"
	"testing"
)

// TestReduceMergesDeployScopedSeries is the saving this whole change rests on:
// the same route reported by two builds is one series once the version is gone.
func TestReduceMergesDeployScopedSeries(t *testing.T) {
	a := `{"service.name":"api","service.version":"abc123","http.route":"/galleries"}`
	b := `{"service.name":"api","service.version":"def456","http.route":"/galleries"}`

	p := DefaultDropPolicy()
	_, fpA := p.Reduce(a, StepHour)
	_, fpB := p.Reduce(b, StepHour)
	if fpA != fpB {
		t.Errorf("fingerprints differ (%s vs %s): service.version still splits the series", fpA, fpB)
	}

	attrs, _ := p.Reduce(a, StepHour)
	var m map[string]string
	if err := json.Unmarshal([]byte(attrs), &m); err != nil {
		t.Fatalf("attributes: %v", err)
	}
	if _, ok := m["service.version"]; ok {
		t.Error("service.version survived the hourly reduction")
	}
	if m["http.route"] != "/galleries" {
		t.Error("http.route must survive: it is what a chart groups by")
	}
}

func TestReduceByTier(t *testing.T) {
	attrs := `{"service.name":"web","http.route":"/x","path":"/x/9182","server.address":"tenant.example.com","telemetry.sdk.version":"1.2.3"}`
	p := DefaultDropPolicy()

	tests := []struct {
		name string
		step int64
		kept []string
		gone []string
	}{
		{
			name: "5m keeps everything",
			step: Step5m,
			kept: []string{"service.name", "http.route", "path", "server.address", "telemetry.sdk.version"},
		},
		{
			name: "hourly drops volatile keys",
			step: StepHour,
			kept: []string{"service.name", "http.route", "path", "server.address"},
			gone: []string{"telemetry.sdk.version"},
		},
		{
			name: "daily also drops raw paths and hosts",
			step: StepDay,
			kept: []string{"service.name", "http.route"},
			gone: []string{"path", "server.address", "telemetry.sdk.version"},
		},
		{
			name: "monthly reduces like daily",
			step: StepMonth,
			kept: []string{"service.name", "http.route"},
			gone: []string{"path", "server.address"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := p.Reduce(attrs, tc.step)
			var m map[string]string
			if err := json.Unmarshal([]byte(out), &m); err != nil {
				t.Fatalf("attributes: %v", err)
			}
			for _, k := range tc.kept {
				if _, ok := m[k]; !ok {
					t.Errorf("%s was dropped but should be kept", k)
				}
			}
			for _, k := range tc.gone {
				if _, ok := m[k]; ok {
					t.Errorf("%s survived", k)
				}
			}
		})
	}
}

func TestReducePrefixWildcard(t *testing.T) {
	p := DropPolicy{Hourly: []string{"telemetry.sdk.*"}}
	out, _ := p.Reduce(`{"telemetry.sdk.name":"go","telemetry.sdk.version":"1","service.name":"api"}`, StepHour)

	var m map[string]string
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("attributes: %v", err)
	}
	if len(m) != 1 || m["service.name"] != "api" {
		t.Errorf("prefix pattern should drop the whole telemetry.sdk family, got %v", m)
	}
}

func TestParseDropList(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"   ", 0},
		{"a.b", 1},
		{"a.b, c.d ,", 2},
	}
	for _, tc := range tests {
		if got := len(ParseDropList(tc.in)); got != tc.want {
			t.Errorf("ParseDropList(%q) = %d keys, want %d", tc.in, got, tc.want)
		}
	}
}
