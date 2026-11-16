package engine

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

var testStorageOptions = StorageOptions{CacheMB: 2, AttachCacheMB: 2}

func openTestStorage(t *testing.T, path string) *Storage {
	t.Helper()
	store, err := OpenStorage(context.Background(), path, testStorageOptions)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func mainHasTable(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	has, err := HasTable(context.Background(), db, table)
	if err != nil {
		t.Fatal(err)
	}
	return has
}

func spansVersion(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow(`SELECT max(version_id) FROM main.` + spansVersionTable).Scan(&v); err != nil {
		t.Fatalf("read spans track version: %v", err)
	}
	return v
}

func TestOpenStorageNewInstallSplits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store := openTestStorage(t, path)
	if !store.Split() {
		t.Fatal("new install did not get a spans file")
	}
	for _, table := range FamilySpans.Tables() {
		if mainHasTable(t, store.Main.DB, table) {
			t.Errorf("main still has %s", table)
		}
		if !mainHasTable(t, store.Spans.DB, table) {
			t.Errorf("spans file lacks %s", table)
		}
	}
	if mainHasTable(t, store.Spans.DB, "projects") {
		t.Error("spans file has a core table")
	}
	if v, want := spansVersion(t, store.Spans.DB), int64(len(spansMigrations())); v != want {
		t.Errorf("spans track version = %d, want %d", v, want)
	}
	var av int
	if err := store.Spans.QueryRow(`PRAGMA main.auto_vacuum`).Scan(&av); err != nil || av != 2 {
		t.Errorf("spans file auto_vacuum = %d (err %v), want 2 (INCREMENTAL)", av, err)
	}
}

func TestOpenStorageKeepsSingleFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	legacy, err := NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(legacy.DB); err != nil {
		t.Fatal(err)
	}
	legacy.Close()

	store := openTestStorage(t, path)
	if store.Split() {
		t.Fatal("a database with spans in main switched to the split layout")
	}
	if _, err := os.Stat(SpansPath(path)); !os.IsNotExist(err) {
		t.Fatalf("spans file created for a single-file database: %v", err)
	}
	if v, want := spansVersion(t, store.Main.DB), int64(len(spansMigrations())); v != want {
		t.Errorf("spans track version in main = %d, want %d", v, want)
	}
}

// Main-track migrations must leave the tables of every family outside core
// alone: in the split layout main does not have the span tables, and the spans
// and shard tracks start from the schema as of spansBaselineVersion. A schema
// change to one of them belongs in spansMigrations or shardMigrations.
func TestMainTrackLeavesSpanTablesAlone(t *testing.T) {
	ctx := context.Background()
	db, err := NewDB(filepath.Join(t.TempDir(), "head.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db.DB); err != nil {
		t.Fatal(err)
	}
	for _, f := range Families()[1:] {
		baseline, err := familyBaselineDDL(ctx, f.Tables())
		if err != nil {
			t.Fatal(err)
		}
		head := familySchema(t, db.DB, f.Tables())
		if len(head) != len(baseline) {
			t.Fatalf("head has %d %s objects, baseline %d", len(head), f, len(baseline))
		}
		for i := range baseline {
			if want := createPrefix.ReplaceAllStringFunc(head[i], ifNotExists); want != baseline[i] {
				t.Errorf("%s schema changed after version %d:\nhead:     %s\nbaseline: %s", f, spansBaselineVersion, want, baseline[i])
			}
		}
	}
}

func familySchema(t *testing.T, db *sql.DB, tables []string) []string {
	t.Helper()
	args := make([]any, len(tables))
	for i, tbl := range tables {
		args[i] = tbl
	}
	rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE tbl_name IN (`+placeholderList(len(tables))+`)
		AND sql IS NOT NULL ORDER BY type = 'index', rowid`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}
