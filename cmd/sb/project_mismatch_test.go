package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A key bound to one project lists only that project. Asking for another one
// with --project used to fall through to the key's project and print its data
// as if it were the asked project's.
func projectsServer(t *testing.T, listed ...map[string]any) (*Client, *int) {
	t.Helper()
	queried := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/projects" {
			_ = json.NewEncoder(w).Encode(listed)
			return
		}
		queried++
		_ = json.NewEncoder(w).Encode([]any{})
	}))
	t.Cleanup(srv.Close)
	return &Client{base: srv.URL, http: srv.Client()}, &queried
}

func TestQueryRefusesAProjectOtherThanTheKeys(t *testing.T) {
	c, queried := projectsServer(t, map[string]any{"id": 5, "slug": "profotograaf-prod"})
	c.project = "brandtrace"
	_, err := c.query("/api/v1/prompts", url.Values{})
	if err == nil {
		t.Fatal("a project other than the key's was queried")
	}
	for _, want := range []string{"brandtrace", "profotograaf-prod"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if *queried != 0 {
		t.Errorf("the data endpoint was called %d times", *queried)
	}
}

func TestQueryAllowsTheKeysOwnProject(t *testing.T) {
	c, queried := projectsServer(t, map[string]any{"id": 5, "slug": "profotograaf-prod"})
	c.project = "profotograaf-prod"
	if _, err := c.query("/api/v1/prompts", url.Values{}); err != nil || *queried != 1 {
		t.Fatalf("err = %v, queried = %d, want a normal query", err, *queried)
	}
}

func TestQueryNamesAnUnknownProjectForAKeyWithSeveral(t *testing.T) {
	c, _ := projectsServer(t, map[string]any{"id": 5, "slug": "a"}, map[string]any{"id": 6, "slug": "b"})
	c.project = "nope"
	_, err := c.query("/api/v1/prompts", url.Values{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want project not found", err)
	}
}

func TestQueryWithoutAProjectStillQueries(t *testing.T) {
	c, queried := projectsServer(t)
	if _, err := c.query("/api/v1/prompts", url.Values{}); err != nil || *queried != 1 {
		t.Fatalf("err = %v, queried = %d", err, *queried)
	}
}
