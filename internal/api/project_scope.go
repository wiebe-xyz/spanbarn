package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// ctxProjectNames carries a lookup from project id to slug, so a refusal can
// name the projects it is about.
const ctxProjectNames contextKey = "projectNames"

// withProjectNames puts the slug lookup in the request context. It runs ahead
// of the authentication middleware, which writes the project mismatch error.
func withProjectNames(projects projectLookup) func(http.Handler) http.Handler {
	slug := func(id int64) string {
		if p, err := projects.ByID(id); err == nil {
			return p.Slug
		}
		return ""
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxProjectNames, slug)))
		})
	}
}

// describeProject renders "slug (id N)", or "id N" when the slug is unknown.
func describeProject(ctx context.Context, id int64) string {
	n := strconv.FormatInt(id, 10)
	if slug, _ := ctx.Value(ctxProjectNames).(func(int64) string); slug != nil {
		if s := slug(id); s != "" {
			return s + " (project " + n + ")"
		}
	}
	return "project " + n
}

// writeProjectMismatch refuses a request that names a project other than the
// one the API key is bound to, naming both by slug and id.
func writeProjectMismatch(w http.ResponseWriter, r *http.Request, keyProject, asked int64) {
	writeError(w, http.StatusForbidden, "project mismatch",
		"this API key belongs to "+describeProject(r.Context(), keyProject)+", the request names "+describeProject(r.Context(), asked))
}

// projectKeyMayReach reports whether the caller may address project id. A key
// bound to a project reaches only that project; everyone else is unrestricted.
func projectKeyMayReach(w http.ResponseWriter, r *http.Request, id int64) bool {
	if bound := GetProjectID(r.Context()); bound != 0 && bound != id {
		writeProjectMismatch(w, r, bound, id)
		return false
	}
	return true
}

// filterProjectsForKey keeps only the key's own project for a project-bound key.
func filterProjectsForKey(ctx context.Context, projects []service.Project) []service.Project {
	bound := GetProjectID(ctx)
	if bound == 0 {
		return projects
	}
	out := make([]service.Project, 0, 1)
	for _, p := range projects {
		if p.ID == bound {
			out = append(out, p)
		}
	}
	return out
}

// filterStatsForKey keeps only the key's own project's usage for a bound key.
func filterStatsForKey(bound int64, stats []service.ProjectUsageStats) []service.ProjectUsageStats {
	if bound == 0 {
		return stats
	}
	out := make([]service.ProjectUsageStats, 0, 1)
	for _, s := range stats {
		if s.ProjectID == bound {
			out = append(out, s)
		}
	}
	return out
}
