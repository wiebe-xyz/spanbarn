package repository

import (
	"context"
	"database/sql"
	"time"
)

// rollupStep5m is the step of the metric_rollups table (rollup.Step5m, which
// this package cannot import). ScanProjectRollups reads any other step from
// metric_rollups_coarse.
const rollupStep5m = 300

// projectRollupColumns is coarseColumns with extra kept only for the types
// that read it (histogram buckets, quantiles). A gauge or sum row then costs
// no JSON copy.
const projectRollupColumns = `project_id, name, type, unit, temporality, attr_fingerprint, attributes, bucket,
	count, sum, min, max, last, obs_count,
	CASE WHEN type IN ('histogram', 'exp_histogram', 'summary') THEN extra END`

// ScanProjectRollups streams every series of one tier for a project in
// [from, to] to fn, ordered by name, fingerprint and bucket, so a series
// arrives as one contiguous run. A 300s step reads metric_rollups and
// any other step the coarse tier of that width.
//
// It pins each table's (project, bucket) index. Left to the planner, the
// 5-minute read walked the project's whole history in name order on the
// lookup index and filtered on bucket. The caller's deadline bounds the scan,
// or the per-query timeout when it has none; fn returning an error stops it.
func (r *Repository) ScanProjectRollups(ctx context.Context, projectID, step int64, from, to time.Time, fn func(MetricRollup) error) error {
	ctx, cancel := r.readContext(ctx)
	defer cancel()

	q, args := projectRollupScan(projectID, step, from, to)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var m MetricRollup
		var extra sql.NullString
		if err := rows.Scan(
			&m.ProjectID, &m.Name, &m.Type, &m.Unit, &m.Temporality, &m.AttrFingerprint, &m.Attributes, &m.Bucket,
			&m.Count, &m.Sum, &m.Min, &m.Max, &m.Last, &m.ObsCount, &extra,
		); err != nil {
			return err
		}
		m.Extra = extra.String
		if err := fn(m); err != nil {
			return err
		}
	}
	return rows.Err()
}

// projectRollupScan returns ScanProjectRollups' query for a tier.
func projectRollupScan(projectID, step int64, from, to time.Time) (string, []any) {
	if step == rollupStep5m {
		return `SELECT ` + projectRollupColumns + `
			FROM metric_rollups INDEXED BY idx_metric_rollups_bucket
			WHERE project_id = ? AND bucket >= ? AND bucket <= ?
			ORDER BY name, attr_fingerprint, bucket`, []any{projectID, from, to}
	}
	return `SELECT ` + projectRollupColumns + `
		FROM metric_rollups_coarse INDEXED BY idx_metric_rollups_coarse_bucket
		WHERE project_id = ? AND step_seconds = ? AND bucket >= ? AND bucket <= ?
		ORDER BY name, attr_fingerprint, bucket`, []any{projectID, step, from, to}
}
