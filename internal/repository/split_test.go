package repository

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// testLayoutEnv selects the storage layout setupTestDB builds. "split" puts
// every family in its own file, each writer attaching the others read-only, so
// a write that reaches another family's table fails the test that made it.
// CI runs the package once per layout.
const testLayoutEnv = "SPANBARN_TEST_LAYOUT"

func splitTestLayout() bool { return os.Getenv(testLayoutEnv) == "split" }

// setupSplitTestRepo builds a repository whose families live in separate files
// under a test temp dir. Each file is migrated in full and then drops the
// tables it does not own.
func setupSplitTestRepo(t *testing.T) *Repository {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, numFamilies)
	for _, f := range Families() {
		paths[f] = filepath.Join(dir, f.String()+".db")
		createFamilyTestFile(t, paths[f], f)
	}
	var repo *Repository
	for _, f := range Families() {
		var attach []Attachment
		for _, o := range Families() {
			if o != f {
				attach = append(attach, Attachment{Schema: "fam_" + o.String(), Path: paths[o]})
			}
		}
		db, err := Open(paths[f], OpenOptions{CacheMB: 2, MmapMB: 0, Attach: attach})
		if err != nil {
			t.Fatalf("open %s: %v", f, err)
		}
		t.Cleanup(func() { db.Close() })
		if f == FamilyCore {
			repo = NewRepository(db.DB)
			continue
		}
		repo.SetFamilyWriter(f, db.DB, nil)
	}
	return repo
}

func createFamilyTestFile(t *testing.T, path string, f Family) {
	t.Helper()
	db, err := NewDB(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer db.Close()
	if err := Migrate(db.DB); err != nil {
		t.Fatalf("migrate %s: %v", path, err)
	}
	tables, err := userTables(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		if tbl == "goose_db_version" || TableFamily(tbl) == f {
			continue
		}
		if _, err := db.Exec("DROP TABLE " + tbl); err != nil {
			t.Fatalf("drop %s from %s: %v", tbl, path, err)
		}
	}
}

func userTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// TestSplitLayoutRoutesWrites checks the harness itself: a family writer can
// write its own tables, cannot write another family's, and the read handle
// sees every table.
func TestSplitLayoutRoutesWrites(t *testing.T) {
	repo := setupSplitTestRepo(t)
	if _, err := repo.writer(FamilySpans).Exec(`INSERT INTO aggregates
		(project_id, service, operation, kind, bucket, count)
		VALUES (1, 's', 'o', 'server', '2026-10-04 00:00:00', 1)`); err != nil {
		t.Fatalf("spans writer, own table: %v", err)
	}
	if _, err := repo.writer(FamilySpans).Exec(`INSERT INTO settings (key, value) VALUES ('k', 'v')`); err == nil {
		t.Fatal("spans writer wrote a core table")
	}
	if _, err := repo.writer(FamilyCore).Exec(`DELETE FROM aggregates`); err == nil {
		t.Fatal("core writer wrote a span-family table")
	}
	var n int
	if err := repo.DB().QueryRow(`SELECT COUNT(*) FROM aggregates`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read handle sees aggregates: n=%d err=%v", n, err)
	}
}
