package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
)

type codedErr struct{ code int }

func (e codedErr) Error() string { return fmt.Sprintf("sqlite code %d", e.code) }
func (e codedErr) Code() int     { return e.code }

// flakyDriver fails the first `failures` queries with code, then succeeds.
type flakyDriver struct {
	code     int
	failures int32
	calls    atomic.Int32
	opens    atomic.Int32
}

func (d *flakyDriver) Open(string) (driver.Conn, error) {
	d.opens.Add(1)
	return &flakyConn{d: d}, nil
}

type flakyConn struct{ d *flakyDriver }

func (c *flakyConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *flakyConn) Close() error                        { return nil }
func (c *flakyConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (c *flakyConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	if c.d.calls.Add(1) <= c.d.failures {
		return nil, codedErr{c.d.code}
	}
	return &oneRow{}, nil
}

type oneRow struct{ done bool }

func (r *oneRow) Columns() []string { return []string{"n"} }
func (r *oneRow) Close() error      { return nil }
func (r *oneRow) Next(dest []driver.Value) error {
	if r.done {
		return fmt.Errorf("eof")
	}
	r.done = true
	dest[0] = int64(1)
	return nil
}

func flakyDB(d *flakyDriver) *sql.DB {
	return sql.OpenDB(recoverConnector{inner: recoverDriverImpl{inner: d}})
}

type recoverConnector struct{ inner recoverDriverImpl }

func (c recoverConnector) Connect(context.Context) (driver.Conn, error) { return c.inner.Open("") }
func (c recoverConnector) Driver() driver.Driver                        { return c.inner }

func TestReadonlyRecoveryIsRetriedOnFreshConnection(t *testing.T) {
	d := &flakyDriver{code: sqliteReadonlyRecovery, failures: 2}
	db := flakyDB(d)
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT 1`).Scan(&n); err != nil {
		t.Fatalf("query failed after retries: %v", err)
	}
	if got := d.opens.Load(); got < 2 {
		t.Fatalf("opened %d connections, want a fresh one per retry", got)
	}
}

func TestOtherErrorsAreNotRetried(t *testing.T) {
	d := &flakyDriver{code: 1, failures: 5}
	db := flakyDB(d)
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT 1`).Scan(&n); err == nil {
		t.Fatal("expected the error to surface")
	}
	if got := d.calls.Load(); got != 1 {
		t.Fatalf("query ran %d times, want 1", got)
	}
}

func TestRecoverConnDelegatesEveryOperation(t *testing.T) {
	db, err := sql.Open(recoverDriver(), "file:"+filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE t (v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	st, err := tx.PrepareContext(ctx, `INSERT INTO t VALUES (?)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ExecContext(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRowContext(ctx, `SELECT v FROM t`).Scan(&v); err != nil || v != 3 {
		t.Fatalf("v=%d err=%v", v, err)
	}
	if _, err := db.ExecContext(ctx, `bogus sql`); err == nil {
		t.Fatal("want a syntax error to pass through")
	}
}

func TestRecoverErrGivesUpWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := codedErr{sqliteReadonlyRecovery}
	if got := recoverErr(ctx, in); got != error(in) {
		t.Fatalf("got %v, want the original error", got)
	}
}

func TestReadOnlyHandleUsesRecoveringDriver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	w, err := Open(path, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`CREATE TABLE t (v INTEGER); INSERT INTO t VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	r, err := Open(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	var v int
	if err := r.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil || v != 7 {
		t.Fatalf("v=%d err=%v", v, err)
	}
}
