package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// MetricRollup is one downsampled metric series bucket. See migration 025 for
// how the columns map onto each OTLP metric type.
type MetricRollup struct {
	ProjectID       int64
	Name            string
	Type            string
	Unit            string
	AttrFingerprint string
	Attributes      string // canonical JSON of the label set
	Bucket          time.Time
	Count           int64
	Sum             float64
	Min             float64
	Max             float64
	Last            float64
	ObsCount        int64
	Extra           string // merged histogram / last summary quantiles
}

// MetricRollupFilter scopes a rollup query.
type MetricRollupFilter struct {
	ProjectID  int64
	Name       string
	From       time.Time
	To         time.Time
	Attributes map[string]string // label equality filters via JSON_EXTRACT
	Limit      int
}

// UpsertMetricRollups persists a batch of rollup buckets in a single
// transaction. Counts and sums accumulate on conflict (so a late straggler for
// an already-written bucket still folds in); min/max take the extreme; last and
// extra are replaced by the newest flush, which is correct because the
// accumulator only emits a bucket once it is closed.
func (r *Repository) UpsertMetricRollups(rollups []MetricRollup) error {
	if len(rollups) == 0 {
		return nil
	}
	return r.execLow(func() error {
		tx, err := r.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		stmt, err := tx.Prepare(`INSERT INTO metric_rollups
			(project_id, name, type, unit, attr_fingerprint, attributes, bucket,
			 count, sum, min, max, last, obs_count, extra)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(project_id, name, attr_fingerprint, bucket)
			DO UPDATE SET
				count = count + excluded.count,
				sum = sum + excluded.sum,
				min = MIN(min, excluded.min),
				max = MAX(max, excluded.max),
				last = excluded.last,
				obs_count = obs_count + excluded.obs_count,
				extra = excluded.extra`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, m := range rollups {
			attrs := m.Attributes
			if attrs == "" {
				attrs = "{}"
			}
			var extra *string
			if m.Extra != "" {
				extra = &m.Extra
			}
			if _, err := stmt.Exec(
				m.ProjectID, m.Name, m.Type, m.Unit, m.AttrFingerprint, attrs, m.Bucket,
				m.Count, m.Sum, m.Min, m.Max, m.Last, m.ObsCount, extra,
			); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// QueryMetricRollups returns rollup buckets for a metric name, ordered by bucket
// ascending so the derivation layer can compute rates across consecutive buckets.
func (r *Repository) QueryMetricRollups(ctx context.Context, f MetricRollupFilter) ([]MetricRollup, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	where := []string{"project_id = ?", "name = ?", "bucket >= ?", "bucket <= ?"}
	args := []any{f.ProjectID, f.Name, f.From, f.To}
	where, args, err := appendAttrFilters(where, args, f.Attributes)
	if err != nil {
		return nil, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 50000 {
		limit = 10000
	}
	args = append(args, limit)

	q := fmt.Sprintf(`SELECT project_id, name, type, unit, attr_fingerprint, attributes, bucket,
		count, sum, min, max, last, obs_count, extra
		FROM metric_rollups WHERE %s ORDER BY bucket ASC LIMIT ?`,
		strings.Join(where, " AND "))

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MetricRollup
	for rows.Next() {
		var m MetricRollup
		var extra sql.NullString
		if err := rows.Scan(
			&m.ProjectID, &m.Name, &m.Type, &m.Unit, &m.AttrFingerprint, &m.Attributes, &m.Bucket,
			&m.Count, &m.Sum, &m.Min, &m.Max, &m.Last, &m.ObsCount, &extra,
		); err != nil {
			return nil, err
		}
		if extra.Valid {
			m.Extra = extra.String
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// QueryProjectRollups returns every rollup bucket for a project within a time
// range, across all metric names, ordered by name then bucket ascending. Used
// by insight detection to scan all series at once.
func (r *Repository) QueryProjectRollups(ctx context.Context, projectID int64, from, to time.Time, limit int) ([]MetricRollup, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	if limit <= 0 || limit > 200000 {
		limit = 50000
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT project_id, name, type, unit, attr_fingerprint, attributes, bucket,
			count, sum, min, max, last, obs_count, extra
		 FROM metric_rollups
		 WHERE project_id = ? AND bucket >= ? AND bucket <= ?
		 ORDER BY name ASC, attr_fingerprint ASC, bucket ASC LIMIT ?`,
		projectID, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MetricRollup
	for rows.Next() {
		var m MetricRollup
		var extra sql.NullString
		if err := rows.Scan(
			&m.ProjectID, &m.Name, &m.Type, &m.Unit, &m.AttrFingerprint, &m.Attributes, &m.Bucket,
			&m.Count, &m.Sum, &m.Min, &m.Max, &m.Last, &m.ObsCount, &extra,
		); err != nil {
			return nil, err
		}
		if extra.Valid {
			m.Extra = extra.String
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMetricRollupsOlderThan removes rollup buckets older than cutoff, in
// bounded chunks so the write lock is never held long enough to block ingest.
func (r *Repository) DeleteMetricRollupsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	return r.batchedDelete(ctx, func() (int64, error) {
		res, e := r.db.ExecContext(ctx,
			"DELETE FROM metric_rollups WHERE rowid IN (SELECT rowid FROM metric_rollups WHERE bucket < ? LIMIT ?)",
			cutoff, retentionDeleteBatch,
		)
		if e != nil {
			return 0, e
		}
		n, _ := res.RowsAffected()
		return n, nil
	})
}
