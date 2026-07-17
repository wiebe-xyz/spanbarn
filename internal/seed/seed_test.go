package seed

import (
	"errors"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const (
	hashA = "aaaa000000000000000000000000000000000000000000000000000000000001"
	hashB = "bbbb000000000000000000000000000000000000000000000000000000000002"
)

func TestParseEmptyIsNoop(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n"} {
		keys, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		if len(keys) != 0 {
			t.Errorf("Parse(%q) = %d keys, want 0", raw, len(keys))
		}
	}
}

func TestParseValid(t *testing.T) {
	raw := `[{"project":"bugbarn","name":"bugbarn-otlp","scope":"ingest","key_sha256":"` + hashA + `"}]`
	keys, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	want := Key{Project: "bugbarn", Name: "bugbarn-otlp", Scope: "ingest", KeySHA256: hashA}
	if keys[0] != want {
		t.Errorf("got %+v, want %+v", keys[0], want)
	}
}

func TestParseDefaultsScopeToIngest(t *testing.T) {
	raw := `[{"project":"p","name":"k","key_sha256":"` + hashA + `"}]`
	keys, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if keys[0].Scope != "ingest" {
		t.Errorf("scope = %q, want ingest", keys[0].Scope)
	}
}

// An uppercase digest is a valid SHA-256 that would never match a presented
// key, since Authorize compares lowercase hex. Normalizing it is the
// difference between a working key and an unexplained 401.
func TestParseLowercasesHash(t *testing.T) {
	raw := `[{"project":"p","name":"k","key_sha256":"` + strings.ToUpper(hashA) + `"}]`
	keys, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if keys[0].KeySHA256 != hashA {
		t.Errorf("hash = %q, want lowercase %q", keys[0].KeySHA256, hashA)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"malformed json", `[{"project":`},
		{"missing project", `[{"name":"k","key_sha256":"` + hashA + `"}]`},
		{"missing name", `[{"project":"p","key_sha256":"` + hashA + `"}]`},
		{"bad scope", `[{"project":"p","name":"k","scope":"admin","key_sha256":"` + hashA + `"}]`},
		{"short hash", `[{"project":"p","name":"k","key_sha256":"abc"}]`},
		{"non-hex hash", `[{"project":"p","name":"k","key_sha256":"zzzz000000000000000000000000000000000000000000000000000000000001"}]`},
		{"duplicate hash", `[{"project":"a","name":"k","key_sha256":"` + hashA + `"},{"project":"b","name":"k2","key_sha256":"` + hashA + `"}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.raw); err == nil {
				t.Errorf("Parse(%s) = nil error, want error", tc.raw)
			}
		})
	}
}

func newTestRepo(t *testing.T) *repository.Repository {
	t.Helper()
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return repository.NewRepository(db.DB)
}

func TestApplyRegistersAndIsIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	keys := []Key{
		{Project: "bugbarn", Name: "bugbarn-otlp", Scope: "ingest", KeySHA256: hashA},
		{Project: "iambarn", Name: "iambarn-otlp", Scope: "ingest", KeySHA256: hashB},
	}

	added, err := Apply(repo, keys, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if added != 2 {
		t.Errorf("added = %d, want 2", added)
	}

	// The seeded key must actually authenticate — that is the whole point.
	rec, err := repo.GetAPIKeyByHash(hashA)
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: %v", err)
	}
	if rec.Scope != "ingest" || rec.Name != "bugbarn-otlp" {
		t.Errorf("got %+v, want scope=ingest name=bugbarn-otlp", rec)
	}
	proj, err := repo.GetProjectBySlug("bugbarn")
	if err != nil {
		t.Fatalf("GetProjectBySlug: %v", err)
	}
	if rec.ProjectID != proj.ID {
		t.Errorf("key project_id = %d, want %d", rec.ProjectID, proj.ID)
	}
	if proj.Status != "active" {
		t.Errorf("project status = %q, want active", proj.Status)
	}

	// Seeding runs on every boot, so a second pass must add nothing.
	added, err = Apply(repo, keys, nil)
	if err != nil {
		t.Fatalf("Apply (second): %v", err)
	}
	if added != 0 {
		t.Errorf("second Apply added = %d, want 0", added)
	}
	all, err := repo.ListAllAPIKeys()
	if err != nil {
		t.Fatalf("ListAllAPIKeys: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("got %d keys after re-seed, want 2", len(all))
	}
}

// Seeding must not resurrect or rename a project an operator has since edited.
func TestApplyLeavesExistingProjectAlone(t *testing.T) {
	repo := newTestRepo(t)
	if _, err := repo.CreateProject("bugbarn", "Bug Barn (renamed)"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	keys := []Key{{Project: "bugbarn", Name: "k", Scope: "ingest", KeySHA256: hashA}}
	if _, err := Apply(repo, keys, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	proj, err := repo.GetProjectBySlug("bugbarn")
	if err != nil {
		t.Fatalf("GetProjectBySlug: %v", err)
	}
	if proj.Name != "Bug Barn (renamed)" {
		t.Errorf("name = %q, want the operator's name preserved", proj.Name)
	}
	projects, err := repo.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("got %d projects, want 1 (no duplicate)", len(projects))
	}
}

type failingStore struct{ onKey bool }

func (f *failingStore) EnsureProject(slug, _ string) (repository.Project, error) {
	if f.onKey {
		return repository.Project{ID: 1}, nil
	}
	return repository.Project{}, errors.New("boom")
}

func (f *failingStore) EnsureAPIKey(int64, string, string, string) (bool, error) {
	return false, errors.New("boom")
}

// A seeding failure must surface, not be swallowed: coming up with an
// incomplete key set means 401ing every client with no explanation.
func TestApplyPropagatesErrors(t *testing.T) {
	keys := []Key{{Project: "p", Name: "k", Scope: "ingest", KeySHA256: hashA}}
	tests := []struct {
		name  string
		store *failingStore
	}{
		{"project error", &failingStore{onKey: false}},
		{"api key error", &failingStore{onKey: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Apply(tc.store, keys, nil); err == nil {
				t.Error("Apply = nil error, want error")
			}
		})
	}
}
