package repository

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// mainWithFreelist creates a split-layout database whose main is in
// auto_vacuum=NONE mode and holds about 8 MB of freelist, the state staging's
// main was in after retention emptied its heavy tables.
func mainWithFreelist(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store, err := OpenStorage(context.Background(), path, testStorageOptions)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	if _, err := store.Repository().CreateProject("proj", "Proj"); err != nil {
		t.Fatal(err)
	}
	db := store.Main.DB
	for _, q := range []string{
		`PRAGMA main.auto_vacuum = NONE`,
		`VACUUM main`,
		`CREATE TABLE main.scratch (b BLOB)`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 2000)
		 INSERT INTO main.scratch SELECT randomblob(4000) FROM n`,
		`DROP TABLE main.scratch`,
		`PRAGMA main.wal_checkpoint(TRUNCATE)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	store.Close()
	return path
}

func pagesOf(t *testing.T, db *sql.DB) pageCounts {
	t.Helper()
	p, err := mainPages(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenStorageCompactsMain(t *testing.T) {
	path := mainWithFreelist(t)

	plain, err := Open(path, OpenOptions{CacheMB: 2})
	if err != nil {
		t.Fatal(err)
	}
	before := pagesOf(t, plain.DB)
	plain.Close()
	if before.autoVacuum == autoVacuumIncremental || before.free < 1000 {
		t.Fatalf("setup: auto_vacuum=%d freelist=%d, want NONE with a freelist", before.autoVacuum, before.free)
	}

	// A reader keeps main open while the writer starts.
	read := openTestReadRepo(t, path)

	o := testStorageOptions
	o.CompactMain = true
	o.CompactMainMaxLiveBytes = 1 << 30
	store, err := OpenStorage(context.Background(), path, o)
	if err != nil {
		t.Fatalf("open storage with compaction: %v", err)
	}
	defer store.Close()

	after := pagesOf(t, store.Main.DB)
	if after.autoVacuum != autoVacuumIncremental {
		t.Errorf("auto_vacuum = %d, want %d", after.autoVacuum, autoVacuumIncremental)
	}
	if after.free != 0 {
		t.Errorf("freelist = %d pages after compaction, want 0", after.free)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() >= before.fileBytes() {
		t.Errorf("main is %d bytes, was %d; want it smaller", fi.Size(), before.fileBytes())
	}
	if _, err := store.Repository().GetProjectBySlug("proj"); err != nil {
		t.Errorf("project after compaction: %v", err)
	}
	if _, err := read.GetProjectBySlug("proj"); err != nil {
		t.Errorf("reader after compaction: %v", err)
	}
}

func TestCompactMainSkipsAboveLimit(t *testing.T) {
	path := mainWithFreelist(t)
	o := testStorageOptions
	o.CompactMain = true
	o.CompactMainMaxLiveBytes = 1
	store, err := OpenStorage(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if p := pagesOf(t, store.Main.DB); p.autoVacuum == autoVacuumIncremental || p.free == 0 {
		t.Errorf("auto_vacuum=%d freelist=%d; a skipped compaction must leave main alone", p.autoVacuum, p.free)
	}
}

func TestCompactMainIsANoOpOnIncrementalMain(t *testing.T) {
	path := mainWithFreelist(t)
	plain, err := Open(path, OpenOptions{CacheMB: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	ctx := context.Background()
	if err := compactMain(ctx, plain.DB, 0, testLogger()); err != nil {
		t.Fatalf("first compaction: %v", err)
	}
	if _, err := plain.Exec(`CREATE TABLE main.again (b BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Exec(`INSERT INTO main.again VALUES (randomblob(100000))`); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Exec(`DROP TABLE main.again`); err != nil {
		t.Fatal(err)
	}
	free := pagesOf(t, plain.DB).free
	if err := compactMain(ctx, plain.DB, 0, testLogger()); err != nil {
		t.Fatalf("second compaction: %v", err)
	}
	// A second VACUUM would have emptied the freelist; the incremental vacuum
	// in the checkpoint loop is what returns these pages.
	if got := pagesOf(t, plain.DB).free; got != free {
		t.Errorf("freelist %d -> %d; an INCREMENTAL main must not be rewritten again", free, got)
	}
}
