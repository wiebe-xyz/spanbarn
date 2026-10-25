package repository

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
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
	// Temporality is the OTLP aggregation temporality ("delta", "cumulative",
	// or empty where the exporter did not say). Compaction differences a
	// cumulative series into per-bucket increases and passes a delta one
	// through, so the two must stay distinguishable.
	Temporality string
	// StepSeconds is the tier's bucket width. Zero means the 5-minute tier in
	// metric_rollups; anything else is a row of metric_rollups_coarse.
	StepSeconds int64
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

// metricRollupChunk caps the rows one rollup upsert writes in one write slot.
// A flush in production holds about 3,000 rows, and each row seeks a different
// leaf of idx_metric_rollups_lookup. In one transaction on a cold page cache
// the flush held main's write slot for 43s after a writer restart on
// 2026-10-23 and 32s on 2026-10-24, past the watchdog's 30s (#371).
const metricRollupChunk = 500

// metricRollupUpsertSQL folds one bucket into its row. Counts and sums
// accumulate on conflict (so a late straggler for an already-written bucket
// still folds in); min/max take the extreme; last and extra are replaced by
// the newest flush, which is correct because the accumulator only emits a
// bucket once it is closed.
const metricRollupUpsertSQL = `INSERT INTO metric_rollups
	(project_id, name, type, unit, temporality, attr_fingerprint, attributes, bucket,
	 count, sum, min, max, last, obs_count, extra)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(project_id, name, attr_fingerprint, bucket)
	DO UPDATE SET
		count = count + excluded.count,
		sum = sum + excluded.sum,
		min = MIN(min, excluded.min),
		max = MAX(max, excluded.max),
		last = excluded.last,
		obs_count = obs_count + excluded.obs_count,
		extra = excluded.extra,
		temporality = excluded.temporality`

// metricRollupWarmSQL reads the row an upsert of the same key would update,
// through the lookup index.
const metricRollupWarmSQL = `SELECT count FROM metric_rollups
	WHERE project_id = ? AND name = ? AND attr_fingerprint = ? AND bucket = ?`

// SetRollupReads gives the rollup upsert a read-only pool on main. With it,
// each chunk first reads its keys on the pool, so the index and table pages
// it updates are in the OS page cache before it takes the write slot.
func (r *Repository) SetRollupReads(db *sql.DB) { r.rollupReads = db }

// UpsertMetricRollups persists a batch of rollup buckets in chunks of
// metricRollupChunk rows in key order, one transaction and one write slot per
// chunk. A chunk that fails stops the batch; the chunks before it stay
// written.
func (r *Repository) UpsertMetricRollups(rollups []MetricRollup) error {
	// Writing telemetry must not emit telemetry. See WithoutSpanTracing.
	ctx := WithoutSpanTracing(context.Background())
	for _, chunk := range metricRollupChunks(rollups) {
		r.warmMetricRollups(ctx, chunk)
		err := r.submit(FamilyCore, writescheduler.Low, "UpsertMetricRollups", func(db *sql.DB) error {
			return upsertMetricRollupChunk(ctx, db, chunk)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// metricRollupChunks sorts a copy of rollups by the lookup index's key, so a
// chunk's seeks walk the index in order, and splits it into chunks.
func metricRollupChunks(rollups []MetricRollup) [][]MetricRollup {
	sorted := slices.Clone(rollups)
	slices.SortFunc(sorted, func(a, b MetricRollup) int {
		return cmp.Or(
			cmp.Compare(a.ProjectID, b.ProjectID),
			strings.Compare(a.Name, b.Name),
			strings.Compare(a.AttrFingerprint, b.AttrFingerprint),
			a.Bucket.Compare(b.Bucket),
		)
	})
	return slices.Collect(slices.Chunk(sorted, metricRollupChunk))
}

// warmMetricRollups reads each key of chunk on the read pool. It only moves
// page reads out of the write slot, so a failed read is left to the upsert.
func (r *Repository) warmMetricRollups(ctx context.Context, chunk []MetricRollup) {
	if r.rollupReads == nil {
		return
	}
	conn, err := r.rollupReads.Conn(ctx)
	if err != nil {
		return
	}
	defer conn.Close()
	stmt, err := conn.PrepareContext(ctx, metricRollupWarmSQL)
	if err != nil {
		return
	}
	defer stmt.Close()
	var count int64
	for _, m := range chunk {
		_ = stmt.QueryRowContext(ctx, m.ProjectID, m.Name, m.AttrFingerprint, m.Bucket).Scan(&count)
	}
}

// upsertMetricRollupChunk writes one chunk in one transaction.
func upsertMetricRollupChunk(ctx context.Context, db *sql.DB, chunk []MetricRollup) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, metricRollupUpsertSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range chunk {
		attrs := m.Attributes
		if attrs == "" {
			attrs = "{}"
		}
		var extra *string
		if m.Extra != "" {
			extra = &m.Extra
		}
		if _, err := stmt.ExecContext(ctx,
			m.ProjectID, m.Name, m.Type, m.Unit, m.Temporality, m.AttrFingerprint, attrs, m.Bucket,
			m.Count, m.Sum, m.Min, m.Max, m.Last, m.ObsCount, extra,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
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

	// Ordered newest-first under the limit, then reversed, so a range wider than
	// the limit keeps its most recent buckets. Ascending order truncated at the
	// limit would return the oldest rows and drop everything current, which is
	// the opposite of what a chart needs.
	q := fmt.Sprintf(`SELECT project_id, name, type, unit, temporality, attr_fingerprint, attributes, bucket,
		count, sum, min, max, last, obs_count, extra
		FROM metric_rollups WHERE %s ORDER BY bucket DESC LIMIT ?`,
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

// scanMetricRollups reads rollup rows in the column order every rollup query
// selects.
func scanMetricRollups(rows *sql.Rows) ([]MetricRollup, error) {
	var out []MetricRollup
	for rows.Next() {
		var m MetricRollup
		var extra sql.NullString
		if err := rows.Scan(
			&m.ProjectID, &m.Name, &m.Type, &m.Unit, &m.Temporality, &m.AttrFingerprint, &m.Attributes, &m.Bucket,
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

// reverseRollups flips a newest-first page back into bucket order, which is what
// rate derivation across consecutive buckets expects.
func reverseRollups(rows []MetricRollup) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
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
		`SELECT project_id, name, type, unit, temporality, attr_fingerprint, attributes, bucket,
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

	return scanMetricRollups(rows)
}

// DeleteMetricRollupsOlderThan removes rollup buckets older than cutoff, in
// bounded chunks so the write lock is never held long enough to block ingest.
func (r *Repository) DeleteMetricRollupsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	// Per project so each batch seeks idx_metric_rollups_project_bucket instead
	// of full-scanning the table (bucket has no standalone index).
	return r.deleteOlderThanPerProject(ctx, "metric_rollups", "bucket", cutoff)
}

// DeleteMetricRollupsOlderThanLimited removes at most max 5-minute rollup rows
// older than cutoff, reporting whether more were left for the next cycle.
func (r *Repository) DeleteMetricRollupsOlderThanLimited(ctx context.Context, cutoff time.Time, max int64) (int64, bool, error) {
	return r.deleteOlderThanPerProjectLimited(ctx, "metric_rollups", "bucket", cutoff, max)
}
