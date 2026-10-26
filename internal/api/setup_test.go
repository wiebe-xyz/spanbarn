package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// TestHandleSetupRejectsOtherMethods: only GET (read) and POST (create) are
// served, and a refused method writes nothing.
func TestHandleSetupRejectsOtherMethods(t *testing.T) {
	s := &Server{logger: slog.Default()}
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(m, "/api/v1/setup/myproj", nil)
		rec := httptest.NewRecorder()
		s.handleSetup(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: want 405, got %d", m, rec.Code)
		}
	}
}

func TestSetupKeyDeterministicAndSecretDependent(t *testing.T) {
	a1, _ := setupKey("secret-A", "proj")
	a2, _ := setupKey("secret-A", "proj")
	b1, _ := setupKey("secret-B", "proj")
	if a1 != a2 {
		t.Errorf("setupKey not deterministic: %q vs %q", a1, a2)
	}
	if a1 == b1 {
		t.Error("setupKey must change when the secret changes")
	}
	if len(a1) != 40 {
		t.Errorf("setup key plaintext length = %d, want 40", len(a1))
	}
}

func setupCall(t *testing.T, s *Server, method, slug string) int {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/setup/"+slug, nil)
	req.SetPathValue("slug", slug)
	rec := httptest.NewRecorder()
	s.handleSetup(rec, req)
	return rec.Code
}

func setupKeys(t *testing.T, repo *repository.Repository) int {
	t.Helper()
	keys, err := repo.ListAllAPIKeys()
	if err != nil {
		t.Fatal(err)
	}
	return len(keys)
}

// GET only reads: an unknown slug is a 404 with no project or key created.
// POST creates the pending project and its key once; a repeat POST and a GET
// render the guide and write nothing.
func TestHandleSetupGetReadsAndPostCreates(t *testing.T) {
	repo := newTestRepo(t)
	s := &Server{logger: slog.Default(), repo: repo, sessionSecret: "test-secret"}

	if code := setupCall(t, s, http.MethodGet, "acme"); code != http.StatusNotFound {
		t.Fatalf("GET unknown slug = %d, want 404", code)
	}
	if _, err := repo.GetProjectBySlug("acme"); err == nil {
		t.Fatal("GET created a project")
	}
	if n := setupKeys(t, repo); n != 0 {
		t.Fatalf("GET registered %d keys", n)
	}

	if code := setupCall(t, s, http.MethodPost, "acme"); code != http.StatusCreated {
		t.Fatalf("POST new slug = %d, want 201", code)
	}
	p, err := repo.GetProjectBySlug("acme")
	if err != nil || p.Status != "pending" {
		t.Fatalf("project after POST = %+v, %v", p, err)
	}
	for _, m := range []string{http.MethodPost, http.MethodGet} {
		if code := setupCall(t, s, m, "acme"); code != http.StatusOK {
			t.Errorf("%s existing slug = %d, want 200", m, code)
		}
	}
	if n := setupKeys(t, repo); n != 1 {
		t.Errorf("%d keys after repeat calls, want 1", n)
	}
}

// Through the router the guide needs a web session: without one, GET and POST
// answer 401 and create nothing.
func TestSetupRouteNeedsASession(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(m, "/api/v1/setup/acme", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", m, rec.Code)
		}
	}
	if _, err := repo.GetProjectBySlug("acme"); err == nil {
		t.Fatal("an unauthenticated request created a project")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/acme", nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST with a session = %d: %s", rec.Code, rec.Body.String())
	}
}

// Migration 045 revokes the setup key of the pending project 'status' and
// leaves every other key, and the project, in place.
func TestMigrationRevokesTheProbeSetupKey(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	probe, err := repo.EnsureProjectPending("status", "status")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.EnsureProjectPending("acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []repository.Project{probe, other} {
		_, sha := setupKey("test-secret", p.Slug)
		if err := repo.EnsureSetupAPIKey(p.ID, sha); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.DB().ExecContext(ctx, `DELETE FROM goose_db_version WHERE version_id = 45`); err != nil {
		t.Fatal(err)
	}
	if err := repository.MigrateSingleFile(ctx, repo.DB()); err != nil {
		t.Fatal(err)
	}
	if keys, _ := repo.ListAPIKeys(probe.ID); len(keys) != 0 {
		t.Errorf("probe project keeps %d keys", len(keys))
	}
	if keys, _ := repo.ListAPIKeys(other.ID); len(keys) != 1 {
		t.Errorf("other project has %d keys, want 1", len(keys))
	}
	if _, err := repo.GetProjectBySlug("status"); err != nil {
		t.Errorf("probe project removed: %v", err)
	}
}
