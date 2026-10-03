package main

import (
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// --- telemetry queries ---

func cmdServices(args []string) error {
	fs := flag.NewFlagSet("services", flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	serverOnly := fs.Bool("server-only", false, "only server-kind services")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	if *serverOnly {
		params.Set("server_only", "true")
	}
	data, err := client.query("/api/v1/services", params)
	if err != nil {
		return err
	}
	return emit(data)
}

func cmdFlows(args []string) error {
	fs := flag.NewFlagSet("flows", flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	service := fs.String("service", "", "filter by service")
	status := fs.String("status", "", "filter by status (e.g. error)")
	errorsOnly := fs.Bool("errors", false, "shortcut for --status=error")
	minDur := fs.Int64("min-duration-us", 0, "minimum root duration (µs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	if *service != "" {
		params.Set("service", *service)
	}
	if *errorsOnly {
		*status = "error"
	}
	if *status != "" {
		params.Set("status", *status)
	}
	if *minDur > 0 {
		params.Set("min_duration_us", strconv.FormatInt(*minDur, 10))
	}
	data, err := client.query("/api/v1/traces/groups", params)
	if err != nil {
		return err
	}
	return emit(data)
}

func cmdTraces(args []string) error {
	fs := flag.NewFlagSet("traces", flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	service := fs.String("service", "", "filter by service")
	operation := fs.String("operation", "", "filter by root operation")
	status := fs.String("status", "", "filter by status (e.g. error)")
	errorsOnly := fs.Bool("errors", false, "shortcut for --status=error")
	minDur := fs.Int64("min-duration-us", 0, "minimum trace duration (µs)")
	rootOnly := fs.Bool("root-only", false, "only return root spans")
	limit := fs.Int("limit", 50, "max traces (<=200)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	if *service != "" {
		params.Set("service", *service)
	}
	if *operation != "" {
		params.Set("operation", *operation)
	}
	if *errorsOnly {
		*status = "error"
	}
	if *status != "" {
		params.Set("status", *status)
	}
	if *minDur > 0 {
		params.Set("min_duration_us", strconv.FormatInt(*minDur, 10))
	}
	if *rootOnly {
		params.Set("root_only", "true")
	}
	params.Set("limit", strconv.Itoa(*limit))
	data, err := client.query("/api/v1/traces", params)
	if err != nil {
		return err
	}
	return emit(data)
}

func cmdTrace(args []string) error {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return fmt.Errorf("usage: sb trace <traceId>")
	}
	client, err := newClient()
	if err != nil {
		return err
	}
	data, err := client.get("/api/v1/traces/" + url.PathEscape(rest[0]))
	if err != nil {
		return err
	}
	return emit(data)
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	traceID := fs.String("trace-id", "", "filter by trace ID")
	spanID := fs.String("span-id", "", "filter by span ID")
	severity := fs.Int("severity", 0, "minimum OTLP severity number (9=INFO,13=WARN,17=ERROR)")
	service := fs.String("service", "", "filter by service")
	search := fs.String("search", "", "body text search")
	limit := fs.Int("limit", 200, "max log entries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	if *traceID != "" {
		params.Set("trace_id", *traceID)
	}
	if *spanID != "" {
		params.Set("span_id", *spanID)
	}
	if *severity > 0 {
		params.Set("severity", strconv.Itoa(*severity))
	}
	if *service != "" {
		params.Set("service", *service)
	}
	if *search != "" {
		params.Set("search", *search)
	}
	params.Set("limit", strconv.Itoa(*limit))
	data, err := client.query("/api/v1/logs", params)
	if err != nil {
		return err
	}
	return emit(data)
}

func cmdMetrics(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sb metrics <names|series> [flags]")
	}
	switch args[0] {
	case "names":
		return cmdMetricNames(args[1:])
	case "series":
		return cmdMetricSeries(args[1:])
	default:
		return fmt.Errorf("unknown metrics subcommand: %s", args[0])
	}
}

func cmdMetricNames(args []string) error {
	return runQueryCmd("metrics names", "/api/v1/metrics/names", args, false)
}

func cmdMetricSeries(args []string) error {
	fs := flag.NewFlagSet("metrics series", flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	name := fs.String("name", "", "metric name (required)")
	limit := fs.Int("limit", 1000, "max points")
	var labels labelFlags
	fs.Var(&labels, "label", "attribute filter key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("--name is required")
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	params.Set("name", *name)
	params.Set("limit", strconv.Itoa(*limit))
	for k, v := range labels {
		params.Set("label["+k+"]", v)
	}
	data, err := client.query("/api/v1/metrics/series", params)
	if err != nil {
		return err
	}
	return emit(data)
}

// labelFlags collects repeated --label key=value flags.
type labelFlags map[string]string

func (l *labelFlags) String() string { return "" }
func (l *labelFlags) Set(v string) error {
	parts := strings.SplitN(v, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("label must be key=value")
	}
	if *l == nil {
		*l = labelFlags{}
	}
	(*l)[parts[0]] = parts[1]
	return nil
}

func cmdPrompts(args []string) error {
	if len(args) > 0 && args[0] == "detail" {
		return cmdPromptDetail(args[1:])
	}
	fs := flag.NewFlagSet("prompts", flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	service := fs.String("service", "", "filter by service")
	model := fs.String("model", "", "filter by model")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	if *service != "" {
		params.Set("service", *service)
	}
	if *model != "" {
		params.Set("model", *model)
	}
	data, err := client.query("/api/v1/prompts", params)
	if err != nil {
		return err
	}
	return emit(data)
}

func cmdPromptDetail(args []string) error {
	fs := flag.NewFlagSet("prompts detail", flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	name := fs.String("name", "", "prompt name (required)")
	model := fs.String("model", "", "filter by model")
	service := fs.String("service", "", "filter by service")
	status := fs.String("status", "", "filter by status")
	finishReason := fs.String("finish-reason", "", "filter by finish reason")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("--name is required")
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	params.Set("name", *name)
	if *model != "" {
		params.Set("model", *model)
	}
	if *service != "" {
		params.Set("service", *service)
	}
	if *status != "" {
		params.Set("status", *status)
	}
	if *finishReason != "" {
		params.Set("finish_reason", *finishReason)
	}
	data, err := client.query("/api/v1/prompts/detail", params)
	if err != nil {
		return err
	}
	return emit(data)
}

func cmdDeps(args []string) error {
	return runQueryCmd("deps", "/api/v1/dependencies", args, true)
}

func cmdDatabase(args []string) error {
	return runQueryCmd("database", "/api/v1/database", args, true)
}

func cmdServiceMap(args []string) error {
	return runQueryCmd("service-map", "/api/v1/service-map", args, false)
}
