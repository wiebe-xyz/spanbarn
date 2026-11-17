package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeResolver struct {
	into  map[int64]int64
	calls int
	err   error
}

func (f *fakeResolver) Resolve(id int64) (int64, error) {
	f.calls++
	if f.err != nil {
		return id, f.err
	}
	if t, ok := f.into[id]; ok {
		return t, nil
	}
	return id, nil
}

func projectSeen(t *testing.T, mw func(http.Handler) http.Handler, target string) string {
	t.Helper()
	var seen string
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.URL.Query().Get("project_id") }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
	return seen
}

func TestMergedProjectsRewriteOldIDs(t *testing.T) {
	res := &fakeResolver{into: map[int64]int64{7: 3}}
	mw := withMergedProjects(res)
	if got := projectSeen(t, mw, "/api/v1/services?project_id=7&x=1"); got != "3" {
		t.Errorf("archived id read as %q, want 3", got)
	}
	for _, target := range []string{"/api/v1/services?project_id=3", "/api/v1/services", "/api/v1/services?project_id=abc", "/api/v1/services?project_id=-2"} {
		want := ""
		if target == "/api/v1/services?project_id=3" {
			want = "3"
		}
		if target == "/api/v1/services?project_id=abc" {
			want = "abc"
		}
		if target == "/api/v1/services?project_id=-2" {
			want = "-2"
		}
		if got := projectSeen(t, mw, target); got != want {
			t.Errorf("%s read as %q, want %q", target, got, want)
		}
	}
}

func TestMergedProjectsCacheAndFailOpen(t *testing.T) {
	res := &fakeResolver{into: map[int64]int64{7: 3}}
	m := &mergedProjects{svc: res, now: time.Now, known: map[int64]resolvedProject{}}
	clock := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	for i := 0; i < 3; i++ {
		if got := m.resolve(7); got != 3 {
			t.Fatalf("resolve = %d", got)
		}
	}
	if res.calls != 1 {
		t.Errorf("lookups = %d, want 1 inside the ttl", res.calls)
	}
	clock = clock.Add(2 * mergedProjectTTL)
	m.resolve(7)
	if res.calls != 2 {
		t.Errorf("lookups = %d, want a second one after the ttl", res.calls)
	}
	res.err = errors.New("db down")
	if got := m.resolve(9); got != 9 {
		t.Errorf("a failed lookup resolved to %d, want the id as asked", got)
	}
}
