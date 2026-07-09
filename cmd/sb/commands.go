package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// commonFlags registers the flags shared by every data command: --output and
// --project. It binds --output directly to the global outputMode.
func commonFlags(fs *flag.FlagSet) *string {
	fs.StringVar(&outputMode, "output", "json", "output format: json|table")
	return fs.String("project", "", "project slug (overrides .spanbarn.json and config default)")
}

// addTimeFlags registers --from/--to and returns their pointers.
func addTimeFlags(fs *flag.FlagSet) (from, to *string) {
	from = fs.String("from", "", "start time (RFC3339 or unix seconds; default: 1h ago)")
	to = fs.String("to", "", "end time (RFC3339 or unix seconds; default: now)")
	return from, to
}

// applyTimeRange sets from/to on params, defaulting to the last hour.
func applyTimeRange(params url.Values, from, to string) {
	if from != "" {
		params.Set("from", from)
	} else {
		params.Set("from", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	}
	if to != "" {
		params.Set("to", to)
	} else {
		params.Set("to", time.Now().UTC().Format(time.RFC3339))
	}
}

// scopedClient builds a client and resolves the project slug to scope queries.
func scopedClient(projectFlag string) (*Client, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	c.project = resolveProject(projectFlag, c.cfg)
	return c, nil
}

// runQueryCmd handles the common CLI shape: the standard project + time-range
// flags, an optional --service filter, then a GET to endpoint and emit.
// Commands with additional flags keep their own implementation.
func runQueryCmd(name, endpoint string, args []string, withService bool) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	var service *string
	if withService {
		service = fs.String("service", "", "filter by service")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	if service != nil && *service != "" {
		params.Set("service", *service)
	}
	data, err := client.query(endpoint, params)
	if err != nil {
		return err
	}
	return emit(data)
}

// --- auth / setup ---

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	urlFlag := fs.String("url", os.Getenv("SPANBARN_URL"), "SpanBarn instance URL")
	apiKey := fs.String("api-key", os.Getenv("SPANBARN_API_KEY"), "read-scoped API key")
	username := fs.String("username", os.Getenv("SPANBARN_USERNAME"), "dashboard username (session login)")
	password := fs.String("password", "", "dashboard password (omit to be prompted)")
	oidc := fs.Bool("oidc", false, "log in via IamBarn device-code flow (browser approval)")
	clientID := fs.String("client-id", os.Getenv("SPANBARN_OIDC_CLIENT_ID"), "IamBarn M2M client id (client_credentials)")
	clientSecret := fs.String("client-secret", os.Getenv("SPANBARN_OIDC_CLIENT_SECRET"), "IamBarn M2M client secret")
	scope := fs.String("scope", "", "OAuth scope for M2M login (optional)")
	project := fs.String("project", "", "default project slug")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *urlFlag == "" {
		return fmt.Errorf("--url is required (or set SPANBARN_URL)")
	}

	cfg := Config{
		URL:     strings.TrimRight(*urlFlag, "/"),
		Project: *project,
	}

	var method string
	switch {
	case *apiKey != "":
		cfg.APIKey = *apiKey
		method = "api-key"
	case *clientID != "" && *clientSecret != "":
		if err := clientCredentialsLogin(&cfg, "", *clientID, *clientSecret, *scope); err != nil {
			return err
		}
		method = "oidc-m2m"
	case *oidc:
		if err := deviceLogin(&cfg); err != nil {
			return err
		}
		method = "oidc-device"
	case *username != "":
		if *password == "" {
			pw, err := promptPassword()
			if err != nil {
				return err
			}
			*password = pw
		}
		token, err := loginWithPassword(cfg.URL, *username, *password)
		if err != nil {
			return err
		}
		cfg.Username = *username
		cfg.Password = *password
		cfg.SessionToken = token
		method = "session"
	default:
		return fmt.Errorf("provide one of: --api-key, --oidc (IamBarn device login), --client-id/--client-secret (M2M), or --username")
	}

	// Verify auth works against a read endpoint.
	client := &Client{base: cfg.URL, http: &http.Client{Timeout: 10 * time.Second}, cfg: cfg}
	if _, err := client.get("/api/v1/projects"); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	if err := saveConfig(client.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Logged in to %s (%s)\n", cfg.URL, method)
	writeOut(map[string]any{"ok": true, "url": cfg.URL, "auth": method})
	return nil
}

// loginWithPassword posts credentials to /api/v1/login and returns the session
// token from the Set-Cookie response.
func loginWithPassword(baseURL, username, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := http.Post(baseURL+"/api/v1/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login failed: HTTP %d", resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "session" {
			return ck.Value, nil
		}
	}
	return "", fmt.Errorf("no session cookie in login response")
}

// promptPassword reads a password from the terminal without echoing.
func promptPassword() (string, error) {
	fmt.Fprint(os.Stderr, "Password: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(pw), nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	projectFlag := fs.String("project", "", "project slug (skip interactive picker)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := newClient()
	if err != nil {
		return err
	}

	slug := *projectFlag
	if slug == "" {
		picked, err := pickProject(client)
		if err != nil {
			return err
		}
		slug = picked
	} else if err := validateProject(client, slug); err != nil {
		return err
	}

	if err := saveLocalConfig(LocalConfig{Project: slug}); err != nil {
		return fmt.Errorf("write %s: %w", localConfigFile, err)
	}
	fmt.Fprintf(os.Stderr, "Project set to %q — saved to %s\n", slug, localConfigFile)
	writeOut(map[string]any{"ok": true, "project": slug, "file": localConfigFile})
	return nil
}

type projectRow struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func listProjects(c *Client) ([]projectRow, error) {
	data, err := c.get("/api/v1/projects")
	if err != nil {
		return nil, err
	}
	var projects []projectRow
	if err := json.Unmarshal(data, &projects); err != nil {
		return nil, fmt.Errorf("parse projects: %w", err)
	}
	return projects, nil
}

func validateProject(c *Client, slug string) error {
	projects, err := listProjects(c)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if p.Slug == slug {
			return nil
		}
	}
	return fmt.Errorf("project %q not found — run 'sb projects' to list available projects", slug)
}

// pickProject prints a numbered list and reads a selection from stdin.
func pickProject(c *Client) (string, error) {
	projects, err := listProjects(c)
	if err != nil {
		return "", err
	}
	if len(projects) == 0 {
		return "", fmt.Errorf("no projects found")
	}
	if len(projects) == 1 {
		return projects[0].Slug, nil
	}
	fmt.Fprintln(os.Stderr, "Select a project:")
	for i, p := range projects {
		fmt.Fprintf(os.Stderr, "  %d) %s (%s)\n", i+1, p.Slug, p.Name)
	}
	fmt.Fprint(os.Stderr, "> ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(projects) {
		return "", fmt.Errorf("invalid selection")
	}
	return projects[n-1].Slug, nil
}

func cmdProjects(args []string) error {
	fs := flag.NewFlagSet("projects", flag.ContinueOnError)
	commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := newClient()
	if err != nil {
		return err
	}
	data, err := client.get("/api/v1/projects")
	if err != nil {
		return err
	}
	return emit(data)
}

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
