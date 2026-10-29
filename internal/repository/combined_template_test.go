package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"testing"
)

// combinedTemplate is one migrated in-memory database per test process,
// serialized. Running both migration tracks for every test is most of
// internal/repository's time under -race, so setupTestDB loads a copy of
// these bytes into each test's own in-memory database instead.
var combinedTemplate struct {
	once sync.Once
	buf  []byte
	err  error
}

// setupCombinedTemplateDB is setupTestDB's single-file layout: a fresh
// in-memory database holding a copy of the migrated template. Tests about the
// migrations themselves use setupCombinedTestDB, which runs them.
func setupCombinedTemplateDB(t *testing.T) *Repository {
	t.Helper()
	combinedTemplate.once.Do(func() {
		combinedTemplate.buf, combinedTemplate.err = buildCombinedTemplate()
	})
	if combinedTemplate.err != nil {
		t.Fatalf("combined template: %v", combinedTemplate.err)
	}
	db, err := NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// Deserialize copies the bytes into SQLite's own allocation, so the
	// template stays unchanged for the next test.
	err = withSerializer(db.DB, func(s sqliteSerializer) error { return s.Deserialize(combinedTemplate.buf) })
	if err != nil {
		t.Fatalf("load combined template: %v", err)
	}
	return NewRepository(db.DB)
}

func buildCombinedTemplate() ([]byte, error) {
	db, err := NewDB(":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := MigrateSingleFile(context.Background(), db.DB); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	var buf []byte
	err = withSerializer(db.DB, func(s sqliteSerializer) error {
		var serr error
		buf, serr = s.Serialize()
		return serr
	})
	return buf, err
}

// sqliteSerializer is the part of the modernc driver connection that copies
// a database in and out of memory.
type sqliteSerializer interface {
	Serialize() ([]byte, error)
	Deserialize([]byte) error
}

// withSerializer runs fn on the handle's driver connection. A writable handle
// has exactly one connection, so for ":memory:" that is the database itself.
func withSerializer(db *sql.DB, fn func(sqliteSerializer) error) error {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(dc any) error {
		if w, ok := dc.(interface{ Raw() driver.Conn }); ok {
			dc = w.Raw()
		}
		s, ok := dc.(sqliteSerializer)
		if !ok {
			return fmt.Errorf("driver connection %T cannot serialize", dc)
		}
		return fn(s)
	})
}

// TestCombinedTemplateMatchesAFreshMigration guards the shortcut: a template
// copy has the same schema, goose versions and auto_vacuum mode as a database
// the migrations just built.
func TestCombinedTemplateMatchesAFreshMigration(t *testing.T) {
	fresh := setupCombinedTestDB(t)
	copied := setupCombinedTemplateDB(t)
	for _, q := range []string{
		`SELECT type || ' ' || name || ' ' || coalesce(sql, '') FROM sqlite_master ORDER BY type, name`,
		`SELECT version_id || ' ' || is_applied FROM goose_db_version ORDER BY id`,
		`PRAGMA auto_vacuum`,
	} {
		want, err := queryStrings(context.Background(), fresh.db, q)
		if err != nil {
			t.Fatalf("fresh %s: %v", q, err)
		}
		got, err := queryStrings(context.Background(), copied.db, q)
		if err != nil {
			t.Fatalf("copied %s: %v", q, err)
		}
		if fmt.Sprint(want) != fmt.Sprint(got) {
			t.Errorf("%s:\n fresh  %v\n copied %v", q, want, got)
		}
		if len(want) == 0 {
			t.Errorf("%s returned no rows", q)
		}
	}
}

// TestCombinedTemplateCopiesAreIndependent checks that each test gets its own
// database: a write in one copy is invisible in another and in the template.
func TestCombinedTemplateCopiesAreIndependent(t *testing.T) {
	a := setupCombinedTemplateDB(t)
	if _, err := a.CreateProject("only-in-a", "A"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	b := setupCombinedTemplateDB(t)
	var n int
	if err := b.db.QueryRow(`SELECT count(*) FROM projects WHERE slug = 'only-in-a'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second copy sees %d rows written in the first", n)
	}
}
