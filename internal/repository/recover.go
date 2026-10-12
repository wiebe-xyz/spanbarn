package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// sqliteReadonlyRecovery is SQLITE_READONLY_RECOVERY (SQLITE_READONLY | 1<<8).
// A read-only connection gets it while another process is running WAL
// recovery on the file, which happens when the writer pod restarts under a
// live reader. The condition clears once recovery finishes, but a connection
// that saw it can keep failing, so the handle is dropped and retried.
const sqliteReadonlyRecovery = 264

const recoverDriverName = "sqlite-recover"

// recoverRetryDelay gives the recovering process a moment before the pool
// opens the replacement connection.
const recoverRetryDelay = 100 * time.Millisecond

var registerRecoverOnce sync.Once

func recoverDriver() string {
	registerRecoverOnce.Do(func() {
		// The connection hooks (attach, setup) live on the registered
		// singleton, so wrap that instance rather than a fresh Driver.
		probe, err := sql.Open("sqlite", "")
		if err != nil {
			panic(err)
		}
		inner := probe.Driver()
		_ = probe.Close()
		sql.Register(recoverDriverName, recoverDriverImpl{inner: inner})
	})
	return recoverDriverName
}

// isReadonlyRecovery reports whether err is SQLITE_READONLY_RECOVERY.
func isReadonlyRecovery(err error) bool {
	var coded interface{ Code() int }
	return errors.As(err, &coded) && coded.Code() == sqliteReadonlyRecovery
}

// recoverErr turns SQLITE_READONLY_RECOVERY into driver.ErrBadConn, which makes
// database/sql discard the connection and retry the statement on a new one.
func recoverErr(ctx context.Context, err error) error {
	if !isReadonlyRecovery(err) {
		return err
	}
	select {
	case <-time.After(recoverRetryDelay):
	case <-ctx.Done():
		return err
	}
	return driver.ErrBadConn
}

type recoverDriverImpl struct{ inner driver.Driver }

func (d recoverDriverImpl) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &recoverConn{Conn: c}, nil
}

type recoverConn struct{ driver.Conn }

func (c *recoverConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var (
		st  driver.Stmt
		err error
	)
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		st, err = p.PrepareContext(ctx, query)
	} else {
		st, err = c.Conn.Prepare(query)
	}
	if err != nil {
		return nil, recoverErr(ctx, err)
	}
	return st, nil
}

func (c *recoverConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var (
		tx  driver.Tx
		err error
	)
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = b.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin() //nolint:staticcheck // fallback for drivers without BeginTx
	}
	if err != nil {
		return nil, recoverErr(ctx, err)
	}
	return tx, nil
}

func (c *recoverConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := q.QueryContext(ctx, query, args)
	if err != nil {
		return nil, recoverErr(ctx, err)
	}
	return rows, nil
}

func (c *recoverConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	res, err := e.ExecContext(ctx, query, args)
	if err != nil {
		return nil, recoverErr(ctx, err)
	}
	return res, nil
}

func (c *recoverConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *recoverConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *recoverConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}
