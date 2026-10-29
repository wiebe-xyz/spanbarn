package repository

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// upsertAggregateSQL adds a row's counts into its bucket. weighted_count and
// weighted_spans add like count, so a bucket whose stored value is NULL (rows
// from before the columns existed) stays NULL, and WeightedEstimate reports it
// as unknown instead of mixing weighted and unweighted spans.
const upsertAggregateSQL = `INSERT INTO aggregates
	(project_id, service, operation, resource, kind, bucket, count, error_count, p50_us, p95_us, p99_us, max_us, sum_duration_us, weighted_count, weighted_spans)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(project_id, service, operation, resource, kind, bucket)
	DO UPDATE SET
		count = count + excluded.count,
		error_count = error_count + excluded.error_count,
		p50_us = excluded.p50_us,
		p95_us = excluded.p95_us,
		p99_us = excluded.p99_us,
		max_us = MAX(max_us, excluded.max_us),
		sum_duration_us = sum_duration_us + excluded.sum_duration_us,
		weighted_count = weighted_count + excluded.weighted_count,
		weighted_spans = weighted_spans + excluded.weighted_spans`

func upsertAggregateArgs(agg Aggregate) []any {
	return []any{
		agg.ProjectID, agg.Service, agg.Operation, agg.Resource, agg.Kind,
		agg.Bucket, agg.Count, agg.ErrorCount,
		agg.P50Us, agg.P95Us, agg.P99Us, agg.MaxUs, agg.SumDurationUs,
		agg.WeightedCount, agg.WeightedSpans,
	}
}

func (r *Repository) UpsertAggregate(agg Aggregate) error {
	// Writing telemetry must not emit telemetry. See WithoutSpanTracing.
	ctx := WithoutSpanTracing(context.Background())
	return r.execLow(FamilySpans, func(db *sql.DB) error {
		_, err := db.ExecContext(ctx, upsertAggregateSQL, upsertAggregateArgs(agg)...)
		return err
	})
}

// aggregateChunk caps the rows one aggregate upsert writes in one write slot.
// Each row seeks idx_agg_lookup and rewrites its idx_agg_stats entry, about
// 4.5 to 5.9 ms a row on production's cold disk, so flushes of 612 to 1,213
// rows held the spans writer for 4.5 to 9.4 s on 2026-10-29. A chunk of 150
// stays under a second.
const aggregateChunk = 150

// UpsertAggregates adds a batch of aggregate rows into their buckets, in
// idx_agg_lookup key order, one transaction and one write slot per chunk of
// aggregateChunk rows. A chunk that fails stops the batch and the chunks
// before it stay written. Retention persists a batch before it deletes the
// spans, so after such a failure its next cycle aggregates the same spans
// again and adds the written chunks a second time.
func (r *Repository) UpsertAggregates(aggs []Aggregate) error {
	return writeTierRows(r, context.Background(), "UpsertAggregates", upsertAggregateSQL, aggs, aggregateChunk, compareAggregateKey, upsertAggregateArgs)
}

// compareAggregateKey orders aggregates by idx_agg_lookup.
func compareAggregateKey(a, b Aggregate) int {
	return cmp.Or(
		cmp.Compare(a.ProjectID, b.ProjectID),
		strings.Compare(a.Service, b.Service),
		strings.Compare(a.Operation, b.Operation),
		strings.Compare(a.Resource, b.Resource),
		strings.Compare(a.Kind, b.Kind),
		a.Bucket.Compare(b.Bucket),
	)
}

func (r *Repository) QueryAggregates(f AggregateFilter) ([]Aggregate, error) {
	var where []string
	var args []any

	if f.ProjectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Service != "" {
		where = append(where, "service = ?")
		args = append(args, f.Service)
	}
	if f.Operation != "" {
		where = append(where, "operation = ?")
		args = append(args, f.Operation)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if !f.From.IsZero() {
		where = append(where, "bucket >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where = append(where, "bucket <= ?")
		args = append(args, f.To)
	}

	q := "SELECT id, project_id, service, operation, resource, kind, bucket, count, error_count, p50_us, p95_us, p99_us, max_us, sum_duration_us, COALESCE(weighted_count, 0), COALESCE(weighted_spans, 0) FROM aggregates"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY bucket DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Aggregate
	for rows.Next() {
		var a Aggregate
		if err := rows.Scan(
			&a.ID, &a.ProjectID, &a.Service, &a.Operation, &a.Resource, &a.Kind,
			&a.Bucket, &a.Count, &a.ErrorCount,
			&a.P50Us, &a.P95Us, &a.P99Us, &a.MaxUs, &a.SumDurationUs,
			&a.WeightedCount, &a.WeightedSpans,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) DeleteAggregatesOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	return r.batchedDelete(ctx, FamilySpans, func(db *sql.DB) (int64, error) {
		res, e := db.ExecContext(ctx,
			"DELETE FROM aggregates WHERE rowid IN (SELECT rowid FROM aggregates WHERE bucket < ? LIMIT ?)",
			cutoff, retentionDeleteBatch,
		)
		if e != nil {
			return 0, e
		}
		n, _ := res.RowsAffected()
		return n, nil
	})
}
