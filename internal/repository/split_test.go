package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// testLayoutEnv selects the storage layout setupTestDB builds. "split" puts
// every family in its own file, each writer attaching the others read-only, so
// a write that reaches another family's table fails the test that made it.
// CI runs the package once per layout.
const testLayoutEnv = "SPANBARN_TEST_LAYOUT"

func splitTestLayout() bool { return os.Getenv(testLayoutEnv) == "split" }

// setupSplitTestRepo builds a repository whose families live in separate files
// under a test temp dir. Each test writes its own copy of the family files, so
// tests stay independent; the files come migrated from splitTemplateFiles.
func setupSplitTestRepo(t *testing.T) *Repository {
	t.Helper()
	templates, err := splitTemplateFiles(t)
	if err != nil {
		t.Fatalf("split layout templates: %v", err)
	}
	dir := t.TempDir()
	paths := make([]string, numFamilies)
	for _, f := range Families() {
		paths[f] = filepath.Join(dir, f.String()+".db")
		if err := os.WriteFile(paths[f], templates[f], 0o600); err != nil {
			t.Fatalf("copy %s: %v", f, err)
		}
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

// splitTemplates holds each family's migrated file, built once per test
// process. Migrating five files in every test made the split pass about four
// times slower than the default one.
var splitTemplates struct {
	once  sync.Once
	files [numFamilies][]byte
	err   error
}

// splitTemplateFiles returns the migrated family files. The first caller's
// temp dir holds them while they are built; after that only the bytes remain.
func splitTemplateFiles(t *testing.T) ([numFamilies][]byte, error) {
	splitTemplates.once.Do(func() {
		dir := t.TempDir()
		for _, f := range Families() {
			path := filepath.Join(dir, f.String()+".db")
			if err := createFamilyFile(path, f); err != nil {
				splitTemplates.err = fmt.Errorf("create %s: %w", f, err)
				return
			}
			b, err := readClosedDBFile(path)
			if err != nil {
				splitTemplates.err = fmt.Errorf("read %s: %w", f, err)
				return
			}
			splitTemplates.files[f] = b
		}
	})
	return splitTemplates.files, splitTemplates.err
}

// createFamilyFile migrates a file in full, drops the tables family f does
// not own, and folds the WAL back into the file before closing it.
func createFamilyFile(path string, f Family) error {
	db, err := NewDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := MigrateSingleFile(context.Background(), db.DB); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	tables, err := userTables(db.DB)
	if err != nil {
		return err
	}
	for _, tbl := range tables {
		if tbl == "goose_db_version" || TableFamily(tbl) == f {
			continue
		}
		if _, err := db.Exec("DROP TABLE " + tbl); err != nil {
			return fmt.Errorf("drop %s: %w", tbl, err)
		}
	}
	_, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// readClosedDBFile reads a closed database file, and refuses one whose WAL
// still holds frames, since a copy of the main file alone would lose them.
func readClosedDBFile(path string) ([]byte, error) {
	if st, err := os.Stat(path + "-wal"); err == nil && st.Size() > 0 {
		return nil, fmt.Errorf("%s-wal still holds %d bytes", path, st.Size())
	}
	return os.ReadFile(path)
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

// Each split repository writes its own copy of the family files, so a write
// in one, to a core or a family table, is invisible to the next.
func TestSplitLayoutCopiesAreIndependent(t *testing.T) {
	a := setupSplitTestRepo(t)
	if _, err := a.CreateProject("only-in-a", "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.writer(FamilySpans).Exec(`INSERT INTO aggregates
		(project_id, service, operation, kind, bucket, count)
		VALUES (1, 's', 'o', 'server', '2026-10-04 00:00:00', 1)`); err != nil {
		t.Fatal(err)
	}

	b := setupSplitTestRepo(t)
	if _, err := b.GetProjectBySlug("only-in-a"); err == nil {
		t.Error("the second repository sees the first one's project")
	}
	var n int
	if err := b.DB().QueryRow(`SELECT COUNT(*) FROM aggregates`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the second repository sees %d aggregates (err %v), want 0", n, err)
	}
}
