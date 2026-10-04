package repository

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func openTestReadRepo(t *testing.T, path string) *Repository {
	t.Helper()
	db, err := OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatalf("open read db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewReadOnlyRepository(db.DB)
}

func mainHasTable(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	has, err := hasTable(context.Background(), db, table)
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

// insertAndReadSpan writes one span through the write repo, refreshes its
// trace structure and reads it back through a fresh read handle, both through
// a query pinned with INDEXED BY and a plain one.
func insertAndReadSpan(t *testing.T, store *Storage, path string) {
	t.Helper()
	repo := store.Repository()
	p, err := repo.CreateProject("proj", "Proj")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dashSpan(t, repo, "s1", "", "GET /x", "web", `{"http.response.status_code": 500}`, 10, at)
	if _, err := repo.writer(FamilySpans).Exec(`UPDATE spans SET project_id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.RefreshTraceStructure(context.Background(), []TraceKey{{ProjectID: p.ID, TraceID: "t-s1"}}); err != nil {
		t.Fatalf("refresh trace structure: %v", err)
	}

	read := openTestReadRepo(t, path)
	pts, err := read.QueryDashboardCounts(SpanFilter{ProjectID: p.ID}, 3600, DashboardGroupService, 5)
	if err != nil {
		t.Fatalf("dashboard counts: %v", err)
	}
	if len(pts) != 1 || pts[0].Count != 1 {
		t.Fatalf("dashboard counts = %+v, want one span", pts)
	}
	var n int
	if err := read.DB().QueryRow(`SELECT count(*) FROM trace_summaries WHERE trace_id = 't-s1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("trace summary through read handle: n=%d err=%v", n, err)
	}
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
	if v := spansVersion(t, store.Spans.DB); v != 1 {
		t.Errorf("spans track version = %d, want 1", v)
	}
	var av int
	if err := store.Spans.QueryRow(`PRAGMA main.auto_vacuum`).Scan(&av); err != nil || av != 2 {
		t.Errorf("spans file auto_vacuum = %d (err %v), want 2 (INCREMENTAL)", av, err)
	}
	insertAndReadSpan(t, store, path)
}

func TestOpenStorageReopensSplit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	openTestStorage(t, path).Close()
	store := openTestStorage(t, path)
	if !store.Split() {
		t.Fatal("reopened split database fell back to one file")
	}
	insertAndReadSpan(t, store, path)
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
	if v := spansVersion(t, store.Main.DB); v != 1 {
		t.Errorf("spans track version in main = %d, want 1", v)
	}
	insertAndReadSpan(t, store, path)
}

// A spans file next to a main that still holds span rows means a half-done
// or hand-made layout. Opening must refuse: main's tables would shadow the
// spans file and hide every span written to it.
func TestOpenStorageRefusesSpanRowsInMain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	legacy, err := NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(legacy.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO spans (project_id, trace_id, span_id, name, service, start_time_us, duration_us)
		VALUES (1, 't', 's', 'n', 'svc', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	if err := os.WriteFile(SpansPath(path), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStorage(context.Background(), path, testStorageOptions); err == nil ||
		!strings.Contains(err.Error(), "cut-over") {
		t.Fatalf("OpenStorage = %v, want a cut-over error", err)
	}
}

func TestStorageCheckpointsEveryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store := openTestStorage(t, path)
	insertAndReadSpan(t, store, path)
	store.FinalCheckpoint(testLogger())
	for _, f := range []string{path, SpansPath(path)} {
		info, err := os.Stat(f + "-wal")
		if err == nil && info.Size() != 0 {
			t.Errorf("%s-wal is %d bytes after the final checkpoint", f, info.Size())
		}
	}
}

func TestDBSpaceCoversSpansFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store := openTestStorage(t, path)
	insertAndReadSpan(t, store, path)
	store.FinalCheckpoint(testLogger())

	space, err := store.Repository().DBSpace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var mainPages, spansPages int64
	if err := store.Main.QueryRow(`PRAGMA main.page_count`).Scan(&mainPages); err != nil {
		t.Fatal(err)
	}
	if err := store.Spans.QueryRow(`PRAGMA main.page_count`).Scan(&spansPages); err != nil {
		t.Fatal(err)
	}
	if space.PageCount != mainPages+spansPages {
		t.Errorf("PageCount = %d, want main %d + spans %d", space.PageCount, mainPages, spansPages)
	}
	var wantBytes int64
	for _, f := range []string{path, SpansPath(path)} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		wantBytes += info.Size()
	}
	if space.FileBytes != wantBytes {
		t.Errorf("FileBytes = %d, want %d (both files)", space.FileBytes, wantBytes)
	}

	readSpace, err := openTestReadRepo(t, path).DBSpace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if readSpace.PageCount != space.PageCount {
		t.Errorf("read handle PageCount = %d, write handle %d", readSpace.PageCount, space.PageCount)
	}
}

// A settings snapshot restored into an empty directory starts as a split
// database: the writer creates the spans file, keeps the settings and serves
// span writes and reads.
func TestSnapshotRestoreStartsAndServes(t *testing.T) {
	src := filepath.Join(t.TempDir(), "spanbarn.db")
	srcStore := openTestStorage(t, src)
	if _, err := srcStore.Repository().CreateProject("kept", "Kept"); err != nil {
		t.Fatal(err)
	}
	srcStore.FinalCheckpoint(testLogger())

	restored := filepath.Join(t.TempDir(), "spanbarn.db")
	if _, err := SnapshotSettings(context.Background(), src, restored); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, err := os.Stat(SpansPath(restored)); !os.IsNotExist(err) {
		t.Fatalf("snapshot wrote a spans file: %v", err)
	}

	store := openTestStorage(t, restored)
	if !store.Split() {
		t.Fatal("restored snapshot did not get a spans file")
	}
	if _, err := store.Repository().GetProjectBySlug("kept"); err != nil {
		t.Fatalf("settings lost in restore: %v", err)
	}
	read := openTestReadRepo(t, restored)
	var n int
	if err := read.DB().QueryRow(`SELECT count(*) FROM spans`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("spans on restored database: n=%d err=%v", n, err)
	}
	if err := store.Repository().InsertSpans([]Span{makeSpan(1, "t", "s", "web", "GET /", "ok", 5)}); err != nil {
		t.Fatalf("insert on restored database: %v", err)
	}
}

// Main-track migrations must leave span-family tables alone: in the split
// layout main does not have them, and the spans track's baseline is the schema
// as of spansBaselineVersion. A span-family schema change belongs in
// spansMigrations.
func TestMainTrackLeavesSpanTablesAlone(t *testing.T) {
	ctx := context.Background()
	baseline, err := spansBaselineDDL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db, err := NewDB(filepath.Join(t.TempDir(), "head.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db.DB); err != nil {
		t.Fatal(err)
	}
	head := familySchema(t, db.DB)
	if len(head) != len(baseline) {
		t.Fatalf("head has %d span-family objects, baseline %d", len(head), len(baseline))
	}
	for i := range baseline {
		if want := createPrefix.ReplaceAllStringFunc(head[i], ifNotExists); want != baseline[i] {
			t.Errorf("span-family schema changed after version %d:\nhead:     %s\nbaseline: %s", spansBaselineVersion, want, baseline[i])
		}
	}
}

func familySchema(t *testing.T, db *sql.DB) []string {
	t.Helper()
	tables := FamilySpans.Tables()
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

// A reader that opens before the writer has created the database fails its
// queries until the files exist, then serves without being reopened.
func TestReadDBRecoversWhenWriterStartsLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	read := openTestReadRepo(t, path)
	var n int
	if err := read.DB().QueryRow(`SELECT count(*) FROM spans`).Scan(&n); err == nil {
		t.Fatal("query succeeded before the database existed")
	}

	store := openTestStorage(t, path)
	if err := store.Repository().InsertSpans([]Span{makeSpan(1, "t", "s", "web", "GET /", "ok", 5)}); err != nil {
		t.Fatal(err)
	}
	if err := read.DB().QueryRow(`SELECT count(*) FROM spans`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read after writer start: n=%d err=%v", n, err)
	}
}
