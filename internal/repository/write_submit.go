package repository

import (
	"context"
	"database/sql"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// execHigh submits fn as a high-priority write (admin CRUD) on f's writer. It
// blocks until fn completes. Falls back to a direct call when no scheduler is
// configured (tests, CLI commands).
func (r *Repository) execHigh(f Family, fn func(db *sql.DB) error) error {
	return r.submit(f, writescheduler.High, writeOpLabel(), fn)
}

// execLow submits fn as a low-priority write (background ingest, retention)
// on f's writer.
func (r *Repository) execLow(f Family, fn func(db *sql.DB) error) error {
	return r.submit(f, writescheduler.Low, writeOpLabel(), fn)
}

// execLowRows is execLow for a write that reports how many rows it wrote; the
// scheduler records the count as write.rows.
func (r *Repository) execLowRows(f Family, fn func(db *sql.DB) (int64, error)) (int64, error) {
	return r.submitRows(f, writescheduler.Low, writeOpLabel(), fn)
}

// execIngest submits fn as a telemetry insert of rows rows of f: on the
// current shard of f when f is sharded, otherwise as execLow on f's writer.
// The job reports rows as write.rows once fn succeeds, so a slow insert can
// be read per row.
func (r *Repository) execIngest(f Family, rows int64, fn func(db *sql.DB) error) error {
	rowsFn := func(db *sql.DB) (int64, error) {
		if err := fn(db); err != nil {
			return 0, err
		}
		return rows, nil
	}
	if r.shards.Sharded(f) {
		return r.shards.submit(f, writeOpLabel(), rowsFn)
	}
	_, err := r.submitRows(f, writescheduler.Low, writeOpLabel(), rowsFn)
	return err
}

func (r *Repository) submit(f Family, p writescheduler.Priority, label string, fn func(db *sql.DB) error) error {
	_, err := r.submitRows(f, p, label, func(db *sql.DB) (int64, error) { return writescheduler.NoRows, fn(db) })
	return err
}

// submitRows is submit for a write that reports how many rows it wrote, or
// writescheduler.NoRows. It returns 0 rows for a job that did not run.
func (r *Repository) submitRows(f Family, p writescheduler.Priority, label string, fn func(db *sql.DB) (int64, error)) (int64, error) {
	db := r.writer(f)
	s := r.schedulers[f]
	if s == nil {
		return fn(db)
	}
	n, err := s.SubmitRows(context.Background(), p, label, func() (int64, error) { return fn(db) })
	return max(n, 0), err
}

// execLowAffecting runs a single low-priority write statement on f's writer
// and returns the number of rows it affected. It captures the
// execLow+Exec+RowsAffected boilerplate shared by the retention
// DeleteXOlderThan / DeleteXByY methods. Its label names the method that
// called it and the family, as batchLabel does, so the span delete reads as
// "DeleteSpansByMaxID:spans" in slow-write warnings instead of
// "execLowAffecting".
func (r *Repository) execLowAffecting(f Family, query string, args ...any) (int64, error) {
	// 0 → writeOpLabelAt, 1 → execLowAffecting, 2 → the repo method.
	label := writeOpLabelAt(2) + ":" + f.String()
	return r.submitRows(f, writescheduler.Low, label, func(db *sql.DB) (int64, error) {
		res, e := db.Exec(query, args...)
		if e != nil {
			return 0, e
		}
		n, _ := res.RowsAffected()
		return n, nil
	})
}

// execHighExpectingRows runs a single high-priority write (admin update/delete
// by key) and returns sql.ErrNoRows when it affected no rows, i.e. the target
// row did not exist. Shared by the admin CRUD methods.
func (r *Repository) execHighExpectingRows(f Family, query string, args ...any) error {
	return r.execHigh(f, func(db *sql.DB) error {
		res, err := db.Exec(query, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}
