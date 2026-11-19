package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type fakeProjectLookup map[int64]repository.Project

func (f fakeProjectLookup) ByID(id int64) (repository.Project, error) {
	p, ok := f[id]
	if !ok {
		return repository.Project{}, http.ErrNoCookie
	}
	return p, nil
}

// TestOrganizationScopeMatrix is #557: an organization besides the owner's reads
// only the projects that list it, and only reads.
func TestOrganizationScopeMatrix(t *testing.T) {
	oc := auth.NewOIDCClient(auth.OIDCConfig{RequiredOrganizationID: "org-owner,org-e2e"})
	projects := fakeProjectLookup{
		1: {ID: 1, Slug: "shared", OrganizationIDs: "org-e2e"},
		2: {ID: 2, Slug: "private"},
		3: {ID: 3, Slug: "other", OrganizationIDs: "org-other, org-e2e"},
	}
	gate := withOrganizationScope(projects, func() *auth.OIDCClient { return oc })

	cases := []struct {
		name   string
		org    string
		method string
		target string
		want   int
	}{
		{"owner reads a private project", "org-owner", "GET", "/api/v1/spans?project_id=2", 200},
		{"owner reads the project list", "org-owner", "GET", "/api/v1/projects", 200},
		{"owner reads with no project", "org-owner", "GET", "/api/v1/logs", 200},
		{"owner mutates", "org-owner", "POST", "/api/v1/projects/2/apikeys", 200},
		{"no organization (key or local session)", "", "GET", "/api/v1/spans?project_id=2", 200},
		{"extra org reads a listed project", "org-e2e", "GET", "/api/v1/spans?project_id=1", 200},
		{"extra org reads a project listing it among others", "org-e2e", "GET", "/api/v1/spans?project_id=3", 200},
		{"extra org reads a private project", "org-e2e", "GET", "/api/v1/spans?project_id=2", 403},
		{"extra org reads an unknown project", "org-e2e", "GET", "/api/v1/spans?project_id=99", 403},
		{"extra org reads with no project", "org-e2e", "GET", "/api/v1/logs", 403},
		{"extra org reads project zero", "org-e2e", "GET", "/api/v1/logs?project_id=0", 403},
		{"extra org lists projects", "org-e2e", "GET", "/api/v1/projects", 200},
		{"extra org reaches project management", "org-e2e", "GET", "/api/v1/projects/1/apikeys", 403},
		{"extra org writes to a listed project", "org-e2e", "POST", "/api/v1/alerts?project_id=1", 403},
		{"organization the project does not list", "org-other", "GET", "/api/v1/spans?project_id=1", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, caller := withCaller(httptest.NewRequest(tc.method, tc.target, nil).Context())
			caller.org = tc.org
			req := httptest.NewRequest(tc.method, tc.target, nil).WithContext(ctx)
			rec := httptest.NewRecorder()
			gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s %s as %q: status %d, want %d", tc.method, tc.target, tc.org, rec.Code, tc.want)
			}
		})
	}
}

func TestOrganizationScopeFiltersTheProjectList(t *testing.T) {
	oc := auth.NewOIDCClient(auth.OIDCConfig{RequiredOrganizationID: "org-owner,org-e2e"})
	all := []repository.Project{
		{ID: 1, OrganizationIDs: "org-e2e"}, {ID: 2}, {ID: 3, OrganizationIDs: "org-other,org-e2e"},
	}
	gate := withOrganizationScope(fakeProjectLookup{}, func() *auth.OIDCClient { return oc })
	for org, want := range map[string]int{"org-e2e": 2, "org-owner": 3} {
		ctx, caller := withCaller(httptest.NewRequest("GET", "/api/v1/projects", nil).Context())
		caller.org = org
		req := httptest.NewRequest("GET", "/api/v1/projects", nil).WithContext(ctx)
		got := -1
		gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = len(filterProjectsForOrganization(r.Context(), all))
		})).ServeHTTP(httptest.NewRecorder(), req)
		if got != want {
			t.Fatalf("%s sees %d projects, want %d", org, got, want)
		}
	}
}
