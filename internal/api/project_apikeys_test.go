package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func apiKeyCall(t *testing.T, srv *Server, method, path, body string, c *http.Cookie, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if c != nil {
		req.AddCookie(c)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// repoKeys is the authorizer's view of the repository, as cmd/spanbarn wires it.
type repoKeys struct{ repo *repository.Repository }

func (k repoKeys) GetAPIKeyByHash(hash string) (auth.APIKeyRecord, error) {
	r, err := k.repo.GetAPIKeyByHash(hash)
	return auth.APIKeyRecord{ID: r.ID, ProjectID: r.ProjectID, Scope: r.Scope}, err
}
func (k repoKeys) TouchAPIKey(int64) error { return nil }

func projectWithKey(t *testing.T, repo *repository.Repository, slug, scope string) (repository.Project, string) {
	t.Helper()
	p, err := repo.CreateProject(slug, slug)
	if err != nil {
		t.Fatal(err)
	}
	key, hash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateAPIKey(p.ID, "existing", hash, scope); err != nil {
		t.Fatal(err)
	}
	return p, key
}

// A web session mints a random key, shown once, that authorizes as the
// project's ingest key. Neither an anonymous request nor an API key, the
// full-scope static key included, can mint.
func TestCreateAPIKeyNeedsASessionAndReturnsAWorkingKey(t *testing.T) {
	_, sm, repo := setupMetricsQueryServer(t)
	authorizer := auth.NewAuthorizer(auth.HashKey("test-key"), repoKeys{repo}, slog.Default())
	srv := NewServerWithQuery(ServerConfig{APIKey: "test-key", Version: "test"}, nil, nil, sm, nil,
		WithRepository(repo), WithAuthorizer(authorizer))
	p, readKey := projectWithKey(t, repo, "acme", "read")
	path := "/api/v1/projects/" + strconv.FormatInt(p.ID, 10) + "/apikeys"
	body := `{"name":"ingest-2026-10"}`

	if rec := apiKeyCall(t, srv, http.MethodPost, path, body, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous POST = %d, want 401", rec.Code)
	}
	for _, key := range []string{readKey, "test-key"} {
		if rec := apiKeyCall(t, srv, http.MethodPost, path, body, nil, map[string]string{"X-SpanBarn-Api-Key": key}); rec.Code != http.StatusForbidden {
			t.Errorf("API key POST = %d, want 403", rec.Code)
		}
	}

	rec := apiKeyCall(t, srv, http.MethodPost, path, body, sessionCookie(t, sm), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("session POST = %d: %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var got createdAPIKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "ingest-2026-10" || got.Scope != "ingest" || len(got.Key) != 64 {
		t.Fatalf("response = %+v", got)
	}
	pid, scope, err := authorizer.Authorize(got.Key)
	if err != nil || pid != p.ID || scope != "ingest" {
		t.Fatalf("new key authorizes as project %d scope %q, %v", pid, scope, err)
	}
}

// Requests the API does not mint are refused before anything is written.
func TestCreateAPIKeyRejectsBadRequests(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	p, _ := projectWithKey(t, repo, "acme", "ingest")
	path := "/api/v1/projects/" + strconv.FormatInt(p.ID, 10) + "/apikeys"
	c := sessionCookie(t, sm)

	for _, tc := range []struct {
		path, body string
		want       int
	}{
		{path, `{"name":"x","scope":"full"}`, http.StatusBadRequest},
		{path, `{"name":"setup"}`, http.StatusBadRequest},
		{path, `{"name":"  "}`, http.StatusBadRequest},
		{path, `{"name":"` + strings.Repeat("n", 65) + `"}`, http.StatusBadRequest},
		{path, `not json`, http.StatusBadRequest},
		{"/api/v1/projects/9999/apikeys", `{"name":"x"}`, http.StatusNotFound},
	} {
		if rec := apiKeyCall(t, srv, http.MethodPost, tc.path, tc.body, c, nil); rec.Code != tc.want {
			t.Errorf("POST %s %s = %d, want %d", tc.path, tc.body, rec.Code, tc.want)
		}
	}
	if keys, _ := repo.ListAPIKeys(p.ID); len(keys) != 1 {
		t.Errorf("%d keys after refused requests, want 1", len(keys))
	}
}

// DELETE revokes a key of the project in the path, and only of that project.
func TestRevokeAPIKeyStaysInsideTheProject(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	a, _ := projectWithKey(t, repo, "acme", "ingest")
	b, _ := projectWithKey(t, repo, "other", "ingest")
	keysOf := func(p repository.Project) []repository.APIKey {
		keys, err := repo.ListAPIKeys(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return keys
	}
	aKey, bKey := keysOf(a)[0], keysOf(b)[0]
	at := func(p repository.Project, k repository.APIKey) string {
		return "/api/v1/projects/" + strconv.FormatInt(p.ID, 10) + "/apikeys/" + strconv.FormatInt(k.ID, 10)
	}
	c := sessionCookie(t, sm)

	if rec := apiKeyCall(t, srv, http.MethodDelete, at(a, aKey), "", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous DELETE = %d, want 401", rec.Code)
	}
	if rec := apiKeyCall(t, srv, http.MethodDelete, at(a, bKey), "", c, nil); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE of another project's key = %d, want 404", rec.Code)
	}
	if rec := apiKeyCall(t, srv, http.MethodDelete, "/api/v1/projects/1/apikeys/x", "", c, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("DELETE with a bad key id = %d, want 400", rec.Code)
	}
	if rec := apiKeyCall(t, srv, http.MethodDelete, at(a, aKey), "", c, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	if len(keysOf(a)) != 0 || len(keysOf(b)) != 1 {
		t.Errorf("after DELETE: acme %d keys, other %d; want 0 and 1", len(keysOf(a)), len(keysOf(b)))
	}
}
