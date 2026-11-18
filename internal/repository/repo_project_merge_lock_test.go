package repository

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openLockTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func lockTestDBs(t *testing.T) (merge, writer *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lock.db")
	merge = openLockTestDB(t, path)
	if _, err := merge.Exec(`CREATE TABLE edges (id INTEGER PRIMARY KEY, project_id INTEGER, n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	return merge, openLockTestDB(t, path)
}

// A deferred transaction that reads, sees another process commit, and then
// writes fails at once with a busy error. This is the failure the first
// production merge hit at span_edges, kept here as the control for the fix.
func TestDeferredBatchTransactionFailsWhenAWriterCommitsBetweenReadAndWrite(t *testing.T) {
	merge, writer := lockTestDBs(t)
	tx, err := merge.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM edges`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`INSERT INTO edges (project_id, n) VALUES (1, 1)`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`INSERT INTO edges (project_id, n) VALUES (2, 1)`)
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("write after a concurrent commit = %v, want database is locked", err)
	}
}

// beginMergeTx holds the write lock from its first statement, so a writer in
// another handle waits for it, and the batch's read-then-write never sees a
// commit in between.
func TestBeginMergeTxHoldsTheWriteLockBeforeTheFirstRead(t *testing.T) {
	merge, writer := lockTestDBs(t)
	tx, err := beginMergeTx(merge, "edges")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	_, err = writer.Exec(`INSERT INTO edges (project_id, n) VALUES (1, 1)`)
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("writer while the merge transaction is open = %v, want database is locked (lock not held)", err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM edges`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO edges (project_id, n) VALUES (2, 1)`); err != nil {
		t.Fatalf("write after the read: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`INSERT INTO edges (project_id, n) VALUES (3, 1)`); err != nil {
		t.Fatalf("writer after the commit: %v", err)
	}
}
