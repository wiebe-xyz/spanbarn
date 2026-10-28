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

// coarseRollupChunk caps the rows one coarse upsert writes in one write slot.
// A compaction pass wrote a project's merged rows in one job with no warm-up:
// 232 rows took 8.1 s, 314 rows 4.8 s and 266 rows 2.1 s at 17:01 on
// 2026-10-28, 8 to 35 ms a row on a cold page cache. Warmed 5-minute chunks
// cost 3 to 5 ms a row, so a chunk of 100 stays under a second once warmed.
const coarseRollupChunk = 100

// coarseRollupUpsertSQL replaces a coarse bucket with the pass's row.
const coarseRollupUpsertSQL = `INSERT INTO metric_rollups_coarse
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
		extra = excluded.extra`

// coarseRollupWarmSQL reads the row an upsert of the same key would replace,
// through idx_metric_rollups_coarse_lookup.
const coarseRollupWarmSQL = `SELECT count FROM metric_rollups_coarse
	WHERE project_id = ? AND name = ? AND attr_fingerprint = ? AND step_seconds = ? AND bucket = ?`

// UpsertCoarseRollups writes compacted buckets in chunks of coarseRollupChunk
// rows in lookup-key order, each chunk's keys read on the read pool first and
// each chunk in its own transaction and write slot. A compaction pass is
// deterministic — it reads a closed source window and derives the same row every
// time — so a repeated pass replaces its previous output rather than adding to
// it. That is what makes the compactor safe to interrupt: a pass stopped after
// some chunks is simply redone.
func (r *Repository) UpsertCoarseRollups(ctx context.Context, rollups []MetricRollup) error {
	// Writing telemetry must not emit telemetry. See WithoutSpanTracing.
	ctx = WithoutSpanTracing(ctx)
	for _, chunk := range coarseRollupChunks(rollups) {
		r.warmRollupKeys(ctx, coarseRollupWarmSQL, len(chunk), func(i int) []any {
			m := chunk[i]
			return []any{m.ProjectID, m.Name, m.AttrFingerprint, m.StepSeconds, m.Bucket}
		})
		_, err := r.submitRows(FamilyCore, writescheduler.Low, "UpsertCoarseRollups", func(db *sql.DB) (int64, error) {
			return int64(len(chunk)), upsertCoarseRollupChunk(ctx, db, chunk)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// coarseRollupChunks sorts a copy of rollups by the lookup index's key, so a
// chunk's seeks walk the index in order, and splits it into chunks.
func coarseRollupChunks(rollups []MetricRollup) [][]MetricRollup {
	sorted := slices.Clone(rollups)
	slices.SortFunc(sorted, func(a, b MetricRollup) int {
		return cmp.Or(
			cmp.Compare(a.ProjectID, b.ProjectID),
			strings.Compare(a.Name, b.Name),
			strings.Compare(a.AttrFingerprint, b.AttrFingerprint),
			cmp.Compare(a.StepSeconds, b.StepSeconds),
			a.Bucket.Compare(b.Bucket),
		)
	})
	return slices.Collect(slices.Chunk(sorted, coarseRollupChunk))
}

// upsertCoarseRollupChunk writes one chunk in one transaction.
func upsertCoarseRollupChunk(ctx context.Context, db *sql.DB, chunk []MetricRollup) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, coarseRollupUpsertSQL)
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
			m.ProjectID, m.Name, m.Type, m.Unit, m.Temporality, m.AttrFingerprint, attrs,
			m.StepSeconds, m.Bucket, m.Count, m.Sum, m.Min, m.Max, m.Last, m.ObsCount, extra,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
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
		n, _, err := r.batchedDeleteLimited(ctx, FamilyCore, max-total, func(db *sql.DB, limit int64) (int64, error) {
			res, e := db.ExecContext(ctx, q, pid, step, cutoff, limit)
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
		n, err := r.batchedDelete(ctx, FamilyCore, func(db *sql.DB) (int64, error) {
			res, e := db.ExecContext(ctx, q, pid, step, cutoff, retentionDeleteBatch)
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
