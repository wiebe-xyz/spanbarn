package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Rollup5mStep is the width of the tier the ingest accumulator writes. Rows of
// that width live in metric_rollups; every coarser tier lives in
// metric_rollups_coarse, which is why the reads below switch on it.
const Rollup5mStep int64 = 300

// RollupWindow returns the rows of one tier with buckets in [from, to), ordered
// so that a compaction pass can walk series by series: project, then name, then
// fingerprint, then time.
//
// The caller asks for one extra source bucket before the window it is
// compacting. That row is what turns a running total into the increase inside
// the window, and reading it here rather than in a second query keeps the pass
// to a single scan of one index range.
func (r *Repository) RollupWindow(ctx context.Context, step int64, from, to time.Time, limit int) ([]MetricRollup, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	if limit <= 0 || limit > 500000 {
		limit = 200000
	}

	var rows *sql.Rows
	var err error
	if step == Rollup5mStep {
		rows, err = r.db.QueryContext(ctx,
			`SELECT project_id, name, type, unit, temporality, attr_fingerprint, attributes, bucket,
				count, sum, min, max, last, obs_count, extra
			 FROM metric_rollups
			 WHERE bucket >= ? AND bucket < ?
			 ORDER BY project_id, name, attr_fingerprint, bucket LIMIT ?`,
			from, to, limit)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+coarseColumns+`
			 FROM metric_rollups_coarse
			 WHERE step_seconds = ? AND bucket >= ? AND bucket < ?
			 ORDER BY project_id, name, attr_fingerprint, bucket LIMIT ?`,
			step, from, to, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanMetricRollups(rows)
}

// CoarseLastByFingerprint returns the `last` value of one tier's bucket, keyed
// by attribute fingerprint. Compaction continues a counter's running total from
// these, so the tier stays monotonic across bucket boundaries.
func (r *Repository) CoarseLastByFingerprint(ctx context.Context, projectID, step int64, bucket time.Time) (map[string]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	rows, err := r.db.QueryContext(ctx,
		`SELECT attr_fingerprint, last FROM metric_rollups_coarse
		 WHERE project_id = ? AND step_seconds = ? AND bucket = ?`,
		projectID, step, bucket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]float64{}
	for rows.Next() {
		var fp string
		var last float64
		if err := rows.Scan(&fp, &last); err != nil {
			return nil, err
		}
		out[fp] = last
	}
	return out, rows.Err()
}

// OldestRollupBucket returns the earliest bucket held by a tier.
//
// It asks per project rather than globally: every index on these tables leads
// with project_id, so a bare MIN(bucket) scans the table, while one seek per
// project is a handful of B-tree descents. The project list itself comes from a
// loose-index-scan.
func (r *Repository) OldestRollupBucket(ctx context.Context, step int64) (time.Time, bool, error) {
	table := "metric_rollups_coarse"
	if step == Rollup5mStep {
		table = "metric_rollups"
	}
	pids, err := r.distinctProjectIDs(ctx, table)
	if err != nil {
		return time.Time{}, false, err
	}

	// ORDER BY … LIMIT 1 rather than MIN(bucket): both read one end of the same
	// index, but an aggregate returns a value with no column affinity, and the
	// driver then hands back the stored text instead of a time.
	q := `SELECT bucket FROM metric_rollups WHERE project_id = ? ORDER BY bucket ASC LIMIT 1`
	args := func(pid int64) []any { return []any{pid} }
	if step != Rollup5mStep {
		q = `SELECT bucket FROM metric_rollups_coarse WHERE project_id = ? AND step_seconds = ?
		     ORDER BY bucket ASC LIMIT 1`
		args = func(pid int64) []any { return []any{pid, step} }
	}

	var oldest time.Time
	var found bool
	for _, pid := range pids {
		var b time.Time
		if err := r.db.QueryRowContext(ctx, q, args(pid)...).Scan(&b); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return time.Time{}, false, err
		}
		if !found || b.Before(oldest) {
			oldest, found = b, true
		}
	}
	return oldest, found, nil
}

// MetricRollupWatermark reports how far a tier has been compacted into the tier
// above it. Retention reads it before deleting: a tier is only ever dropped up
// to its watermark, so data that has not been rolled up yet survives every
// pressure level.
func (r *Repository) MetricRollupWatermark(ctx context.Context, step int64) (time.Time, bool, error) {
	var through time.Time
	err := r.db.QueryRowContext(ctx,
		`SELECT compacted_through FROM metric_rollup_compaction WHERE step_seconds = ?`, step).
		Scan(&through)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return through, true, nil
}

// SetMetricRollupWatermark advances a tier's watermark. It only ever moves
// forward: a pass that re-ran over an earlier window must not hand retention a
// licence to delete less than it already could.
func (r *Repository) SetMetricRollupWatermark(ctx context.Context, step int64, through time.Time) error {
	ctx = WithoutSpanTracing(ctx)
	return r.execLow(func() error {
		_, err := r.db.ExecContext(ctx,
			`INSERT INTO metric_rollup_compaction (step_seconds, compacted_through, updated_at)
			 VALUES (?, ?, CURRENT_TIMESTAMP)
			 ON CONFLICT(step_seconds) DO UPDATE SET
				compacted_through = MAX(compacted_through, excluded.compacted_through),
				updated_at = CURRENT_TIMESTAMP`,
			step, through.UTC())
		return err
	})
}
