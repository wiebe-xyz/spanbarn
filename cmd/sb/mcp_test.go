package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

type fetchCall struct {
	endpoint string
	params   url.Values
}

// runMCP feeds newline-delimited requests to the server and returns the decoded
// responses plus the fetches the tools made.
func runMCP(t *testing.T, reqs ...string) ([]map[string]any, []fetchCall) {
	t.Helper()
	var calls []fetchCall
	fetch := func(endpoint string, params url.Values) (json.RawMessage, error) {
		calls = append(calls, fetchCall{endpoint, params})
		return json.RawMessage(`{"ok":true}`), nil
	}
	var out bytes.Buffer
	if err := serveMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, fetch); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		resps = append(resps, m)
	}
	return resps, calls
}

func TestMCPInitializeAdvertisesTools(t *testing.T) {
	resps, _ := runMCP(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	res := resps[0]["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v", res["protocolVersion"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("capabilities has no tools: %v", res["capabilities"])
	}
}

func TestMCPListsOnlyReadTools(t *testing.T) {
	resps, _ := runMCP(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := resps[0]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		names[tool["name"].(string)] = true
		if tool["inputSchema"] == nil || tool["description"] == "" {
			t.Fatalf("tool %v lacks schema or description", tool["name"])
		}
	}
	for _, want := range []string{"list_projects", "traces", "trace", "logs", "metric_totals", "metric_series", "operations"} {
		if !names[want] {
			t.Errorf("missing tool %s", want)
		}
	}
}

func TestMCPNotificationGetsNoResponse(t *testing.T) {
	resps, _ := runMCP(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(resps) != 0 {
		t.Fatalf("got %d responses for a notification", len(resps))
	}
}

func TestMCPTraceToolEscapesIDIntoPath(t *testing.T) {
	_, calls := runMCP(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"trace","arguments":{"trace_id":"ab/../cd"}}}`)
	if len(calls) != 1 || calls[0].endpoint != "/api/v1/traces/ab%2F..%2Fcd" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestMCPMetricTotalsPassesGroupByAndLabels(t *testing.T) {
	_, calls := runMCP(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"metric_totals","arguments":{"project":"claude-code","name":"claude_code.cost.usage","group_by":"session.id","labels":{"repo":"spanbarn"},"from":"2026-10-12T00:00:00Z"}}}`)
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	c := calls[0]
	if c.endpoint != "/api/v1/metrics/totals" {
		t.Fatalf("endpoint = %s", c.endpoint)
	}
	for k, want := range map[string]string{
		"name": "claude_code.cost.usage", "group_by": "session.id", "label[repo]": "spanbarn",
		"from": "2026-10-12T00:00:00Z", mcpProjectParam: "claude-code",
	} {
		if got := c.params.Get(k); got != want {
			t.Errorf("param %s = %q, want %q", k, got, want)
		}
	}
	if c.params.Get("to") == "" {
		t.Error("to was not defaulted")
	}
}

func TestMCPRequiredArgumentMissingIsToolError(t *testing.T) {
	resps, calls := runMCP(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"metric_series","arguments":{}}}`)
	res := resps[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("result = %v", res)
	}
	if len(calls) != 0 {
		t.Fatalf("fetched despite missing argument: %+v", calls)
	}
}

func TestMCPUnknownToolAndMethod(t *testing.T) {
	resps, _ := runMCP(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_everything","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
	)
	if resps[0]["result"].(map[string]any)["isError"] != true {
		t.Fatalf("unknown tool: %v", resps[0])
	}
	if resps[1]["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("unknown method: %v", resps[1])
	}
}
