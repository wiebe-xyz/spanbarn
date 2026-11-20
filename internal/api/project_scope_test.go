package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

func TestProjectKeySeesOnlyItsOwnProject(t *testing.T) {
	all := []service.Project{{ID: 5, Slug: "spanbarn"}, {ID: 81, Slug: "brandtrace"}, {ID: 7, Slug: "other"}}
	cases := map[string]struct {
		bound int64
		want  int
	}{"a bound key": {81, 1}, "a session or the static key": {0, 3}, "a key whose project is gone": {99, 0}}
	for name, c := range cases {
		ctx := SetProjectID(httptest.NewRequest("GET", "/api/v1/projects", nil).Context(), c.bound)
		got := filterProjectsForKey(ctx, all)
		if len(got) != c.want {
			t.Errorf("%s: %d projects, want %d", name, len(got), c.want)
		}
		if c.bound == 81 && got[0].Slug != "brandtrace" {
			t.Errorf("%s: kept %q", name, got[0].Slug)
		}
	}
}

func TestProjectKeyUsageStatsHoldOnlyItsOwnProject(t *testing.T) {
	stats := []service.ProjectUsageStats{{ProjectID: 5, SpanCount: 9}, {ProjectID: 81, SpanCount: 3}}
	if got := filterStatsForKey(81, stats); len(got) != 1 || got[0].SpanCount != 3 {
		t.Errorf("bound stats = %+v", got)
	}
	if got := filterStatsForKey(0, stats); len(got) != 2 {
		t.Errorf("unbound stats = %+v", got)
	}
}

func TestProjectKeyCannotAddressAnotherProject(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/projects/5/apikeys", nil)
	req = req.WithContext(SetProjectID(req.Context(), 81))
	rr := httptest.NewRecorder()
	(&projectHandlers{}).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "project mismatch") {
		t.Errorf("another project = %d %s", rr.Code, rr.Body)
	}
	own := httptest.NewRequest("GET", "/", nil).WithContext(SetProjectID(t.Context(), 81))
	if !projectKeyMayReach(httptest.NewRecorder(), own, 81) {
		t.Error("a key must reach its own project")
	}
	if !projectKeyMayReach(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), 5) {
		t.Error("an unbound caller must reach any project")
	}
}

func TestProjectMismatchNamesBothSlugs(t *testing.T) {
	lookup := fakeProjectLookup{5: {ID: 5, Slug: "spanbarn"}, 81: {ID: 81, Slug: "brandtrace"}}
	h := withProjectNames(lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProjectMismatch(w, r, 5, 81)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	body := rr.Body.String()
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rr.Code)
	}
	for _, want := range []string{"spanbarn (project 5)", "brandtrace (project 81)"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q lacks %q", body, want)
		}
	}
}

func TestProjectMismatchFallsBackToIDsForAnUnknownProject(t *testing.T) {
	rr := httptest.NewRecorder()
	writeProjectMismatch(rr, httptest.NewRequest("GET", "/", nil), 5, 99999)
	if !strings.Contains(rr.Body.String(), "project 5") || !strings.Contains(rr.Body.String(), "project 99999") {
		t.Errorf("body = %s", rr.Body)
	}
}
