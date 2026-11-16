package engine

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openTestReadDB opens a read handle on path the way a reader pod does.
func openTestReadDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatalf("open read db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

// spansWriter opens a new split database and returns the handle that holds
// the spans family.
func spansWriter(t *testing.T) *sql.DB {
	t.Helper()
	return openTestStorage(t, filepath.Join(t.TempDir(), "spanbarn.db")).Spans.DB
}

// createProject inserts one project row into main.
func createProject(t *testing.T, db *sql.DB, slug string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO projects (slug, name) VALUES (?, ?)`, slug, slug); err != nil {
		t.Fatalf("create project %s: %v", slug, err)
	}
}

// hasProject reports whether db holds a project with slug.
func hasProject(t *testing.T, db *sql.DB, slug string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM projects WHERE slug = ?`, slug).Scan(&n); err != nil {
		t.Errorf("read project %s: %v", slug, err)
		return false
	}
	return n == 1
}
