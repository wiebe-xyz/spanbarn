package repository

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CoarseRollupFilter scopes a query against one coarse tier.
type CoarseRollupFilter struct {
	ProjectID   int64
	Name        string
	StepSeconds int64
	From        time.Time
	To          time.Time
	Attributes  map[string]string // label equality filters via JSON_EXTRACT
	Limit       int
}

// coarseColumns is the select list shared by every coarse-tier read; it matches
// scanMetricRollups' column order.
const coarseColumns = `project_id, name, type, unit, temporality, attr_fingerprint, attributes, bucket,
	count, sum, min, max, last, obs_count, extra`

// UpsertCoarseRollups writes compacted buckets. A compaction pass is
// deterministic — it reads a closed source window and derives the same row every
// time — so a repeated pass replaces its previous output rather than adding to
// it. That is what makes the compactor safe to interrupt: a half-finished pass
// is simply redone.
func (r *Repository) UpsertCoarseRollups(ctx context.Context, rollups []MetricRollup) error {
	if len(rollups) == 0 {
		return nil
	}
	// Writing telemetry must not emit telemetry. See WithoutSpanTracing.
	ctx = WithoutSpanTracing(ctx)
	return r.execLow(func() error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		stmt, err := tx.PrepareContext(ctx, `INSERT INTO metric_rollups_coarse
			(project_id, name, type, unit, temporality, attr_fingerprint, attributes,
			 step_seconds, bucket, count, sum, min, max, last, obs_count, extra)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(project_id, name, attr_fingerprint, step_seconds, bucket)
			DO UPDATE SET
				type = excluded.type,
				unit = excluded.unit,
				temporality = excluded.temporality,
				count = excluded.count,
				sum = excluded.sum,
				min = excluded.min,
				max = excluded.max,
				last = excluded.last,
				obs_count = excluded.obs_count,
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
			if _, err := stmt.ExecContext(ctx,
				m.ProjectID, m.Name, m.Type, m.Unit, m.Temporality, m.AttrFingerprint, attrs,
				m.StepSeconds, m.Bucket, m.Count, m.Sum, m.Min, m.Max, m.Last, m.ObsCount, extra,
			); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// QueryCoarseRollups returns buckets of one tier for a metric name, oldest
// first. Like the 5-minute query it pages newest-first internally so a range
// wider than the limit keeps its most recent buckets.
func (r *Repository) QueryCoarseRollups(ctx context.Context, f CoarseRollupFilter) ([]MetricRollup, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	where := []string{"project_id = ?", "name = ?", "step_seconds = ?", "bucket >= ?", "bucket <= ?"}
	args := []any{f.ProjectID, f.Name, f.StepSeconds, f.From, f.To}
	where, args, err := appendAttrFilters(where, args, f.Attributes)
	if err != nil {
		return nil, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 50000 {
		limit = 10000
	}
	args = append(args, limit)

	q := fmt.Sprintf(`SELECT `+coarseColumns+`
		FROM metric_rollups_coarse WHERE %s ORDER BY bucket DESC LIMIT ?`,
		strings.Join(where, " AND "))

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out, err := scanMetricRollups(rows)
	if err != nil {
		return nil, err
	}
	reverseRollups(out)
	return out, nil
}

// QueryProjectCoarseRollups returns every series of one tier for a project in a
// range, across all metric names. Insight detection uses it the way it uses
// QueryProjectRollups for the 5-minute tier.
func (r *Repository) QueryProjectCoarseRollups(ctx context.Context, projectID, step int64, from, to time.Time, limit int) ([]MetricRollup, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	if limit <= 0 || limit > 200000 {
		limit = 50000
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+coarseColumns+`
		 FROM metric_rollups_coarse
		 WHERE project_id = ? AND step_seconds = ? AND bucket >= ? AND bucket <= ?
		 ORDER BY name ASC, attr_fingerprint ASC, bucket ASC LIMIT ?`,
		projectID, step, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanMetricRollups(rows)
}

// DeleteCoarseRollupsOlderThanLimited removes at most max rows of one tier older
// than cutoff, reporting whether more were left for the next cycle.
func (r *Repository) DeleteCoarseRollupsOlderThanLimited(ctx context.Context, step int64, cutoff time.Time, max int64) (int64, bool, error) {
	pids, err := r.distinctProjectIDs(ctx, "metric_rollups_coarse")
	if err != nil {
		return 0, false, err
	}

	const q = `DELETE FROM metric_rollups_coarse WHERE rowid IN (
		SELECT rowid FROM metric_rollups_coarse
		WHERE project_id = ? AND step_seconds = ? AND bucket < ? LIMIT ?)`
	var total int64
	for _, pid := range pids {
		pid := pid
		n, _, err := r.batchedDeleteLimited(ctx, max-total, func(limit int64) (int64, error) {
			res, e := r.db.ExecContext(ctx, q, pid, step, cutoff, limit)
			if e != nil {
				return 0, e
			}
			m, _ := res.RowsAffected()
			return m, nil
		})
		total += n
		if err != nil {
			return total, true, err
		}
		if total >= max {
			return total, true, nil
		}
	}
	return total, false, nil
}

// DeleteCoarseRollupsOlderThan removes buckets of one tier older than cutoff, a
// project at a time so each bounded DELETE seeks
// idx_metric_rollups_coarse_bucket instead of scanning the table.
func (r *Repository) DeleteCoarseRollupsOlderThan(ctx context.Context, step int64, cutoff time.Time) (int64, error) {
	pids, err := r.distinctProjectIDs(ctx, "metric_rollups_coarse")
	if err != nil {
		return 0, err
	}

	const q = `DELETE FROM metric_rollups_coarse WHERE rowid IN (
		SELECT rowid FROM metric_rollups_coarse
		WHERE project_id = ? AND step_seconds = ? AND bucket < ? LIMIT ?)`
	var total int64
	for _, pid := range pids {
		pid := pid
		n, err := r.batchedDelete(ctx, func() (int64, error) {
			res, e := r.db.ExecContext(ctx, q, pid, step, cutoff, retentionDeleteBatch)
			if e != nil {
				return 0, e
			}
			m, _ := res.RowsAffected()
			return m, nil
		})
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
