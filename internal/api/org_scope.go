package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// ctxOrgScope carries the organization whose projects a restricted caller may
// list; the project list filters on it.
const ctxOrgScope contextKey = "orgScope"

type projectLookup interface {
	ByID(id int64) (service.Project, error)
}

// listsOrganization reports whether a project's organization list names org.
func listsOrganization(organizationIDs, org string) bool {
	for _, id := range strings.Split(organizationIDs, ",") {
		if strings.TrimSpace(id) == org {
			return true
		}
	}
	return false
}

// withOrganizationScope limits a signed-in caller from an organization other
// than the owner's (#557). Such a caller reads only: it must name a project
// that lists its organization, may list the projects that do, and cannot reach
// the per-project management routes. The owner organization, API keys (already
// bound to one project) and local sessions keep their reach. It runs inside the
// authentication middleware, which records the caller's organization.
func withOrganizationScope(projects projectLookup, oidcFn func() *auth.OIDCClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, _ := r.Context().Value(ctxCaller).(*callerInfo)
			if c == nil || c.org == "" || oidcFn == nil {
				next.ServeHTTP(w, r)
				return
			}
			oc := oidcFn()
			if oc == nil || c.org == oc.OwnerOrganization() {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeError(w, http.StatusForbidden, oidcAccessDenied, "")
				return
			}
			path := strings.TrimSuffix(r.URL.Path, "/")
			switch {
			case path == "/api/v1/projects":
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxOrgScope, c.org)))
				return
			case strings.HasPrefix(path, "/api/v1/projects/"):
				writeError(w, http.StatusForbidden, oidcAccessDenied, "")
				return
			}
			id, err := strconv.ParseInt(r.URL.Query().Get("project_id"), 10, 64)
			if err != nil || id <= 0 {
				writeError(w, http.StatusForbidden, oidcAccessDenied, "")
				return
			}
			p, err := projects.ByID(id)
			if err != nil || !listsOrganization(p.OrganizationIDs, c.org) {
				writeError(w, http.StatusForbidden, oidcAccessDenied, "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// filterProjectsForOrganization keeps the projects that list the organization
// the request was scoped to; an unscoped request keeps every project.
func filterProjectsForOrganization(ctx context.Context, projects []service.Project) []service.Project {
	org, _ := ctx.Value(ctxOrgScope).(string)
	if org == "" {
		return projects
	}
	out := make([]service.Project, 0, len(projects))
	for _, p := range projects {
		if listsOrganization(p.OrganizationIDs, org) {
			out = append(out, p)
		}
	}
	return out
}
