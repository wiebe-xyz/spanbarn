package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is a thin HTTP client for the SpanBarn read API. It authenticates with
// a read-scoped API key and, when a project slug is configured, scopes queries
// to that project's numeric ID.
type Client struct {
	base      string
	http      *http.Client
	cfg       Config
	project   string // resolved project slug ("" → no scoping)
	projectID int64  // cached resolved ID (0 → not yet resolved / unknown)
}

func newClient() (*Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return &Client{
		base: strings.TrimRight(cfg.URL, "/"),
		http: &http.Client{Timeout: 30 * time.Second},
		cfg:  cfg,
	}, nil
}

// get performs a raw GET and returns the response body as JSON.
func (c *Client) get(path string) (json.RawMessage, error) {
	return c.getRetry(path, false)
}

func (c *Client) getRetry(path string, retried bool) (json.RawMessage, error) {
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	// API key takes precedence; otherwise use the session token as a bearer.
	if c.cfg.APIKey != "" {
		req.Header.Set("X-SpanBarn-Api-Key", c.cfg.APIKey)
	} else if c.cfg.SessionToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.SessionToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Session expired? Re-authenticate once with stored credentials.
	if resp.StatusCode == http.StatusUnauthorized && !retried &&
		c.cfg.APIKey == "" && c.cfg.Username != "" && c.cfg.Password != "" {
		token, lerr := loginWithPassword(c.base, c.cfg.Username, c.cfg.Password)
		if lerr == nil {
			c.cfg.SessionToken = token
			_ = saveConfig(c.cfg)
			return c.getRetry(path, true)
		}
	}

	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return json.RawMessage(body), nil
}

// query performs a GET against a project-scoped endpoint, injecting project_id
// (resolved from the configured slug) when not already set by the caller.
func (c *Client) query(path string, params url.Values) (json.RawMessage, error) {
	if params == nil {
		params = url.Values{}
	}
	if params.Get("project_id") == "" {
		if id, err := c.resolveProjectID(); err == nil && id != 0 {
			params.Set("project_id", strconv.FormatInt(id, 10))
		}
	}
	if enc := params.Encode(); enc != "" {
		path += "?" + enc
	}
	return c.get(path)
}

// resolveProjectID looks up the configured project slug's numeric ID via the
// projects endpoint, caching the result. Returns 0 when no project is set.
func (c *Client) resolveProjectID() (int64, error) {
	if c.projectID != 0 {
		return c.projectID, nil
	}
	if c.project == "" {
		return 0, nil
	}
	data, err := c.get("/api/v1/projects")
	if err != nil {
		return 0, err
	}
	var projects []struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(data, &projects); err != nil {
		return 0, fmt.Errorf("parse projects: %w", err)
	}
	for _, p := range projects {
		if p.Slug == c.project {
			c.projectID = p.ID
			return p.ID, nil
		}
	}
	return 0, fmt.Errorf("project %q not found — run 'sb projects' to list available projects", c.project)
}
