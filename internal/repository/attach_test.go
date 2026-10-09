package repository

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// makeWALFile creates a WAL-mode database with one row at path, then closes it
// so SQLite checkpoints and removes the -wal and -shm files.
func makeWALFile(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('closed')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			t.Fatalf("%s%s still exists after close", path, suffix)
		}
	}
}

func attachedCount(t *testing.T, mainPath, shardPath string) (int, error) {
	t.Helper()
	db, err := Open(mainPath, OpenOptions{ReadOnly: true, Attach: []Attachment{{Schema: "shard_0", Path: shardPath}}})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT count(*) FROM shard_0.t`).Scan(&n)
	return n, err
}

func TestAttachClosedWALShardWithoutSidecars(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(t.TempDir(), "spanbarn.db")
	makeWALFile(t, mainPath)
	shard := filepath.Join(dir, "logs-20261008.db")
	makeWALFile(t, shard)
	// A reader that cannot create files next to the shard (a read-only mount)
	// cannot create the -shm a read-only WAL connection needs.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	n, err := attachedCount(t, mainPath, shard)
	if err != nil {
		t.Fatalf("attach closed shard: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestAttachOpenWALShardSeesLiveWrites(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "spanbarn.db")
	makeWALFile(t, mainPath)
	shard := filepath.Join(dir, "logs-20261009.db")
	makeWALFile(t, shard)

	w, err := sql.Open("sqlite", shard+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	if _, err := w.Exec(`INSERT INTO t VALUES ('live')`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shard + "-shm"); err != nil {
		t.Fatalf("writer should hold an -shm: %v", err)
	}

	n, err := attachedCount(t, mainPath, shard)
	if err != nil {
		t.Fatalf("attach open shard: %v", err)
	}
	if n != 2 {
		t.Fatalf("rows = %d, want 2 (the WAL write must be visible)", n)
	}
}
