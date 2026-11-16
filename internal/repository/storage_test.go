package repository

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

var testStorageOptions = engine.StorageOptions{CacheMB: 2, AttachCacheMB: 2}

func openTestStorage(t *testing.T, path string) *engine.Storage {
	t.Helper()
	store, err := engine.OpenStorage(context.Background(), path, testStorageOptions)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func openTestReadRepo(t *testing.T, path string) *Repository {
	t.Helper()
	db, err := engine.OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatalf("open read db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewReadOnlyRepository(db.DB)
}

// insertAndReadSpan writes one span through the write repo, refreshes its
// trace structure and reads it back through a fresh read handle, both through
// a query pinned with INDEXED BY and a plain one.
func insertAndReadSpan(t *testing.T, store *engine.Storage, path string) {
	t.Helper()
	repo := ForStorage(store)
	p, err := repo.CreateProject("proj", "Proj")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dashSpan(t, repo, "s1", "", "GET /x", "web", `{"http.response.status_code": 500}`, 10, at)
	if _, err := repo.writer(engine.FamilySpans).Exec(`UPDATE spans SET project_id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.RefreshTraceStructure(context.Background(), []TraceKey{{ProjectID: p.ID, TraceID: "t-s1"}}); err != nil {
		t.Fatalf("refresh trace structure: %v", err)
	}

	read := openTestReadRepo(t, path)
	pts, err := read.QueryDashboardCounts(context.Background(), SpanFilter{ProjectID: p.ID}, 3600, DashboardGroupService, 5)
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

func TestOpenStorageReopensSplit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	openTestStorage(t, path).Close()
	store := openTestStorage(t, path)
	if !store.Split() {
		t.Fatal("reopened split database fell back to one file")
	}
	insertAndReadSpan(t, store, path)
}

func TestStorageCheckpointsEveryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store := openTestStorage(t, path)
	insertAndReadSpan(t, store, path)
	store.FinalCheckpoint(testLogger())
	for _, f := range []string{path, engine.SpansPath(path)} {
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

	space, err := ForStorage(store).DBSpace(context.Background(), path)
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
	for _, f := range []string{path, engine.SpansPath(path)} {
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
	if _, err := ForStorage(srcStore).CreateProject("kept", "Kept"); err != nil {
		t.Fatal(err)
	}
	srcStore.FinalCheckpoint(testLogger())

	restored := filepath.Join(t.TempDir(), "spanbarn.db")
	if _, err := engine.SnapshotSettings(context.Background(), src, restored); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, err := os.Stat(engine.SpansPath(restored)); !os.IsNotExist(err) {
		t.Fatalf("snapshot wrote a spans file: %v", err)
	}

	store := openTestStorage(t, restored)
	if !store.Split() {
		t.Fatal("restored snapshot did not get a spans file")
	}
	if _, err := ForStorage(store).GetProjectBySlug("kept"); err != nil {
		t.Fatalf("settings lost in restore: %v", err)
	}
	read := openTestReadRepo(t, restored)
	var n int
	if err := read.DB().QueryRow(`SELECT count(*) FROM spans`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("spans on restored database: n=%d err=%v", n, err)
	}
	if err := ForStorage(store).InsertSpans([]Span{makeSpan(1, "t", "s", "web", "GET /", "ok", 5)}); err != nil {
		t.Fatalf("insert on restored database: %v", err)
	}
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
	if err := ForStorage(store).InsertSpans([]Span{makeSpan(1, "t", "s", "web", "GET /", "ok", 5)}); err != nil {
		t.Fatal(err)
	}
	if err := read.DB().QueryRow(`SELECT count(*) FROM spans`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read after writer start: n=%d err=%v", n, err)
	}
}
