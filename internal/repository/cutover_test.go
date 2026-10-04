package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// legacyDatabase builds a single-file database with spans ids 1..4 (id 5 was
// written and deleted, so the AUTOINCREMENT counter sits above the newest
// row) and two trace summaries.
func legacyDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	legacy, err := NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if err := Migrate(legacy.DB); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := legacy.Exec(`INSERT INTO spans (project_id, trace_id, span_id, name, service, start_time_us, duration_us)
			VALUES (1, 't' || ?, 's' || ?, 'GET /x', 'web', 1, 1)`, i, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.Exec(`DELETE FROM spans WHERE id = 5`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO trace_summaries (project_id, trace_id, ingested_at)
		VALUES (1, 't1', CURRENT_TIMESTAMP), (1, 't2', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	return path
}

func cutoverOptions() StorageOptions {
	o := testStorageOptions
	o.CutOver = true
	o.Logger = testLogger()
	return o
}

func smallBatches(t *testing.T) {
	t.Helper()
	old := cutoverBatchRows
	cutoverBatchRows = 2
	t.Cleanup(func() { cutoverBatchRows = old })
}

func assertSplitWithSpans(t *testing.T, store *Storage, path string) {
	t.Helper()
	if !store.Split() {
		t.Fatal("the cut-over did not switch to the split layout")
	}
	for _, table := range append([]string{spansVersionTable}, FamilySpans.Tables()...) {
		if mainHasTable(t, store.Main.DB, table) {
			t.Errorf("main still holds %s after the cut-over", table)
		}
	}
	var ids string
	if err := store.Spans.QueryRow(`SELECT group_concat(id) FROM (SELECT id FROM main.spans ORDER BY id)`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if ids != "1,2,3,4" {
		t.Errorf("span ids in the spans file = %q, want 1,2,3,4", ids)
	}
	var summaries int
	if err := store.Spans.QueryRow(`SELECT count(*) FROM main.trace_summaries`).Scan(&summaries); err != nil || summaries != 2 {
		t.Errorf("trace summaries in the spans file = %d (%v), want 2", summaries, err)
	}
	res, err := store.Repository().writer(FamilySpans).Exec(`INSERT INTO spans (project_id, trace_id, span_id, name, service, start_time_us, duration_us)
		VALUES (1, 't6', 's6', 'GET /x', 'web', 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := res.LastInsertId(); id != 6 {
		t.Errorf("next span id = %d, want 6 (the counter must carry over)", id)
	}
	var n int
	if err := openTestReadRepo(t, path).DB().QueryRow(`SELECT count(*) FROM spans`).Scan(&n); err != nil || n != 5 {
		t.Errorf("spans through a read handle = %d (%v), want 5", n, err)
	}
	if fileExists(cutoverPath(path)) {
		t.Error("the temporary cut-over file was left behind")
	}
}

func TestCutOverMovesSpansFamily(t *testing.T) {
	smallBatches(t)
	path := legacyDatabase(t)
	store, err := OpenStorage(context.Background(), path, cutoverOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertSplitWithSpans(t, store, path)
	var rows int
	if err := store.Spans.QueryRow(`SELECT rows FROM main.` + cutoverMarkerTable).Scan(&rows); err != nil || rows != 6 {
		t.Errorf("cut-over marker rows = %d (%v), want 6", rows, err)
	}

	store.Close()
	again, err := OpenStorage(context.Background(), path, cutoverOptions())
	if err != nil {
		t.Fatalf("reopen after the cut-over: %v", err)
	}
	defer again.Close()
	if !again.Split() {
		t.Error("reopen fell back to the single-file layout")
	}
}

// A writer that stops after the rename leaves the spans file next to a main
// that still holds the copied rows. The next start drops them.
func TestCutOverFinishesAfterRename(t *testing.T) {
	path := legacyDatabase(t)
	moved, err := cutOverSpans(context.Background(), path, cutoverOptions())
	if err != nil || !moved {
		t.Fatalf("cutOverSpans = %v, %v", moved, err)
	}
	store, err := OpenStorage(context.Background(), path, testStorageOptions)
	if err != nil {
		t.Fatalf("open after an interrupted cut-over: %v", err)
	}
	defer store.Close()
	assertSplitWithSpans(t, store, path)
}

func assertSingleFile(t *testing.T, store *Storage, path string) {
	t.Helper()
	if store.Split() {
		t.Fatal("switched to the split layout")
	}
	if fileExists(SpansPath(path)) {
		t.Error("spans file created")
	}
	var n int
	if err := store.Main.QueryRow(`SELECT count(*) FROM spans`).Scan(&n); err != nil || n != 4 {
		t.Errorf("spans in main = %d (%v), want 4", n, err)
	}
}

func TestCutOverFailureKeepsSingleFile(t *testing.T) {
	path := legacyDatabase(t)
	// A directory in place of the temporary file: the copy cannot open it.
	if err := os.MkdirAll(filepath.Join(cutoverPath(path), "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStorage(context.Background(), path, cutoverOptions())
	if err != nil {
		t.Fatalf("a failed copy must not stop the writer: %v", err)
	}
	defer store.Close()
	assertSingleFile(t, store, path)
}

func TestCutOverPostponedWhenVolumeIsShort(t *testing.T) {
	old := volumeFreeBytes
	volumeFreeBytes = func(string) (int64, error) { return 1024, nil }
	t.Cleanup(func() { volumeFreeBytes = old })

	path := legacyDatabase(t)
	store, err := OpenStorage(context.Background(), path, cutoverOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertSingleFile(t, store, path)
}

func TestCutOverOnlyWhenAsked(t *testing.T) {
	path := legacyDatabase(t)
	store := openTestStorage(t, path)
	assertSingleFile(t, store, path)
}
