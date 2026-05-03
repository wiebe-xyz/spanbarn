package main

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/repository"

	_ "modernc.org/sqlite"
)

// testRepo opens an in-memory SQLite database, runs migrations, and returns a Repository.
func testRepo(t *testing.T) (*repository.Repository, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	if err := repository.Migrate(db); err != nil {
		db.Close()
		t.Fatalf("migrate: %v", err)
	}
	return repository.NewRepository(db), db
}

func TestVersionOutput(t *testing.T) {
	// The version string is set at build time; in tests it defaults to "dev".
	want := "dev"
	if !strings.Contains(Version, want) {
		t.Errorf("Version = %q, want it to contain %q", Version, want)
	}
}

func TestUserCreateAndList(t *testing.T) {
	repo, db := testRepo(t)
	defer db.Close()

	// Create a user.
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := repo.CreateUser("alice", hash); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// List users and verify.
	users, err := repo.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].Username != "alice" {
		t.Errorf("username = %q, want %q", users[0].Username, "alice")
	}

	// Delete the user.
	if err := repo.DeleteUser("alice"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	users, err = repo.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers after delete: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("expected 0 users after delete, got %d", len(users))
	}
}

func TestProjectCreateAndList(t *testing.T) {
	repo, db := testRepo(t)
	defer db.Close()

	slug := slugFromName("My Cool Project")
	if slug != "my-cool-project" {
		t.Fatalf("slugFromName = %q, want %q", slug, "my-cool-project")
	}

	proj, err := repo.CreateProject(slug, "My Cool Project")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if proj.Slug != "my-cool-project" {
		t.Errorf("project slug = %q, want %q", proj.Slug, "my-cool-project")
	}

	projects, err := repo.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].Name != "My Cool Project" {
		t.Errorf("project name = %q, want %q", projects[0].Name, "My Cool Project")
	}
}

func TestAPIKeyCreateAndRevoke(t *testing.T) {
	repo, db := testRepo(t)
	defer db.Close()

	// Create a project first (API keys require a project).
	proj, err := repo.CreateProject("test-proj", "Test Project")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Generate and store an API key.
	plainKey, err := generateAPIKey()
	if err != nil {
		t.Fatalf("generateAPIKey: %v", err)
	}
	if len(plainKey) != 64 {
		t.Fatalf("expected 64 char key, got %d", len(plainKey))
	}

	keyHash := auth.HashKey(plainKey)
	keyID, err := repo.CreateAPIKey(proj.ID, "my-key", keyHash, "ingest")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	// Verify the key can be found by hash.
	found, err := repo.GetAPIKeyByHash(keyHash)
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: %v", err)
	}
	if found.Name != "my-key" {
		t.Errorf("key name = %q, want %q", found.Name, "my-key")
	}
	if found.Scope != "ingest" {
		t.Errorf("key scope = %q, want %q", found.Scope, "ingest")
	}

	// List keys for the project.
	keys, err := repo.ListAPIKeys(proj.ID)
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}

	// List all keys.
	allKeys, err := repo.ListAllAPIKeys()
	if err != nil {
		t.Fatalf("ListAllAPIKeys: %v", err)
	}
	if len(allKeys) != 1 {
		t.Fatalf("expected 1 key in all, got %d", len(allKeys))
	}

	// Revoke the key.
	if err := repo.RevokeAPIKey(keyID); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}

	// Verify it's gone.
	keys, err = repo.ListAPIKeys(proj.ID)
	if err != nil {
		t.Fatalf("ListAPIKeys after revoke: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys after revoke, got %d", len(keys))
	}
}
