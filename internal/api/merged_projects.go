package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// mergedProjectTTL is how long a lookup of a project id is remembered. A merge
// archives a source once and for all, so a minute of staleness only delays the
// redirect of an old link after the archive.
const mergedProjectTTL = time.Minute

// projectResolver maps a project id to the project a request reads.
type projectResolver interface {
	Resolve(id int64) (int64, error)
}

type mergedProjects struct {
	svc projectResolver
	now func() time.Time

	mu    sync.Mutex
	known map[int64]resolvedProject
}

type resolvedProject struct {
	id      int64
	expires time.Time
}

// withMergedProjects rewrites the project_id query parameter of a request that
// names an archived source to the project it was merged into, so a link or a
// script that still carries the old id reads the canonical project (#558). A
// lookup that fails leaves the request as it came.
func withMergedProjects(svc projectResolver) func(http.Handler) http.Handler {
	m := &mergedProjects{svc: svc, now: time.Now, known: map[int64]resolvedProject{}}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.rewrite(r)
			next.ServeHTTP(w, r)
		})
	}
}

func (m *mergedProjects) rewrite(r *http.Request) {
	q := r.URL.Query()
	raw := q.Get("project_id")
	if raw == "" {
		return
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return
	}
	if target := m.resolve(id); target != id {
		q.Set("project_id", strconv.FormatInt(target, 10))
		r.URL.RawQuery = q.Encode()
	}
}

func (m *mergedProjects) resolve(id int64) int64 {
	now := m.now()
	m.mu.Lock()
	hit, ok := m.known[id]
	m.mu.Unlock()
	if ok && now.Before(hit.expires) {
		return hit.id
	}
	target, err := m.svc.Resolve(id)
	if err != nil {
		return id
	}
	m.mu.Lock()
	m.known[id] = resolvedProject{id: target, expires: now.Add(mergedProjectTTL)}
	m.mu.Unlock()
	return target
}
