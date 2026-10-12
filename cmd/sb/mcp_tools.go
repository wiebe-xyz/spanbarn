package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// mcpProjectParam carries the project slug from a tool call to the fetcher,
// which resolves it to a project_id. It is never sent to the server.
const mcpProjectParam = "_project"

type mcpProp struct {
	name, typ, desc string
}

// mcpTool describes one read-only tool backed by one GET endpoint.
type mcpTool struct {
	name, desc, endpoint string
	pathArg              string // argument escaped into the endpoint path, if any
	timed                bool   // accepts project, from and to
	props                []mcpProp
	required             []string
}

var (
	propLimit = mcpProp{"limit", "integer", "maximum rows to return"}
	propSvc   = mcpProp{"service", "string", "filter by service name"}
	propName  = mcpProp{"name", "string", "metric name, for example claude_code.cost.usage"}
	propGroup = mcpProp{"group_by", "string", "comma-separated attribute keys to group by, for example session.id"}
	propLabel = mcpProp{"labels", "object", "attribute filters as {key: value}"}
)

var mcpTools = []mcpTool{
	{name: "list_projects", desc: "List the projects (slug and id) this account can read.", endpoint: "/api/v1/projects"},
	{name: "services", desc: "Per-service error rate and latency percentiles.", endpoint: "/api/v1/services", timed: true},
	{name: "flows", desc: "Problematic flows grouped by root operation (errors, latency).", endpoint: "/api/v1/traces/groups", timed: true,
		props: []mcpProp{propSvc, {"status", "string", "filter by status, for example error"}, {"min_duration_us", "integer", "minimum root duration in microseconds"}}},
	{name: "traces", desc: "Search traces.", endpoint: "/api/v1/traces", timed: true,
		props: []mcpProp{propSvc, {"operation", "string", "filter by root operation"}, {"status", "string", "filter by status, for example error"},
			{"min_duration_us", "integer", "minimum trace duration in microseconds"}, {"root_only", "string", "true to return root spans only"}, propLimit}},
	{name: "trace", desc: "The full span tree of one trace.", endpoint: "/api/v1/traces/", pathArg: "trace_id",
		props: []mcpProp{{"trace_id", "string", "trace id"}}, required: []string{"trace_id"}},
	{name: "logs", desc: "Query logs and events. Filter a Claude Code session with labels {\"session.id\": \"...\"}.", endpoint: "/api/v1/logs", timed: true,
		props: []mcpProp{{"trace_id", "string", "filter by trace id"}, {"span_id", "string", "filter by span id"},
			{"severity", "integer", "minimum OTLP severity number (9 info, 13 warn, 17 error)"}, propSvc,
			{"search", "string", "body text search"}, {"order", "string", "asc or desc"}, propLabel, propLimit}},
	{name: "metric_names", desc: "Names of the metrics a project has reported.", endpoint: "/api/v1/metrics/names", timed: true},
	{name: "metric_series", desc: "Time series of one metric.", endpoint: "/api/v1/metrics/series", timed: true,
		props: []mcpProp{propName, propGroup, propLabel, propLimit}, required: []string{"name"}},
	{name: "metric_totals", desc: "Total of one metric over the range, optionally grouped. For example cost per session: name claude_code.cost.usage, group_by session.id.",
		endpoint: "/api/v1/metrics/totals", timed: true, props: []mcpProp{propName, propGroup, propLabel}, required: []string{"name"}},
	{name: "operations", desc: "Per-operation span counts, errors and p50/p95/p99 for one service. For Claude Code, service claude-code shows tool calls and blocked-on-user time.",
		endpoint: "/api/v1/services/{service}/operations", pathArg: "service", timed: true,
		props: []mcpProp{{"service", "string", "service name"}, {"kind", "string", "span kind filter, empty for all kinds"}}, required: []string{"service"}},
	{name: "prompts", desc: "LLM/prompt samples.", endpoint: "/api/v1/prompts", timed: true},
	{name: "dependencies", desc: "Service dependency graph.", endpoint: "/api/v1/dependencies", timed: true, props: []mcpProp{propSvc}},
}

func findMCPTool(name string) (mcpTool, bool) {
	for _, t := range mcpTools {
		if t.name == name {
			return t, true
		}
	}
	return mcpTool{}, false
}

// schema renders the tool's JSON Schema input description.
func (t mcpTool) schema() map[string]any {
	props := map[string]any{}
	add := func(p mcpProp) {
		s := map[string]any{"type": p.typ, "description": p.desc}
		if p.typ == "object" {
			s["additionalProperties"] = map[string]any{"type": "string"}
		}
		props[p.name] = s
	}
	if t.timed {
		add(mcpProp{"project", "string", "project slug; defaults to the project in the sb config"})
		add(mcpProp{"from", "string", "start time, RFC3339 or unix seconds; default 1 hour ago"})
		add(mcpProp{"to", "string", "end time, RFC3339 or unix seconds; default now"})
	}
	for _, p := range t.props {
		add(p)
	}
	req := t.required
	if req == nil {
		req = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": req}
}

// request turns tool arguments into the endpoint and query parameters.
func (t mcpTool) request(args map[string]any) (string, url.Values, error) {
	for _, r := range t.required {
		if argString(args, r) == "" {
			return "", nil, fmt.Errorf("argument %q is required", r)
		}
	}
	endpoint := t.endpoint
	params := url.Values{}
	if t.pathArg != "" {
		esc := url.PathEscape(argString(args, t.pathArg))
		if strings.Contains(endpoint, "{"+t.pathArg+"}") {
			endpoint = strings.Replace(endpoint, "{"+t.pathArg+"}", esc, 1)
		} else {
			endpoint += esc
		}
	}
	if t.timed {
		applyTimeRange(params, argString(args, "from"), argString(args, "to"))
		if p := argString(args, "project"); p != "" {
			params.Set(mcpProjectParam, p)
		}
	}
	for _, p := range t.props {
		if p.name == t.pathArg {
			continue
		}
		copyArg(params, args, p)
	}
	return endpoint, params, nil
}

func copyArg(params url.Values, args map[string]any, p mcpProp) {
	if p.typ == "object" {
		labels, _ := args[p.name].(map[string]any)
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			params.Set("label["+k+"]", fmt.Sprint(labels[k]))
		}
		return
	}
	if v := argString(args, p.name); v != "" {
		params.Set(p.name, v)
	}
}

// argString reads a string or number argument as text.
func argString(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprint(v)
	}
}
