package repository

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (r *Repository) UpsertAggregate(agg Aggregate) error {
	// Writing telemetry must not emit telemetry. See WithoutSpanTracing.
	ctx := WithoutSpanTracing(context.Background())
	return r.execLow(func() error {
		_, err := r.db.ExecContext(ctx, `INSERT INTO aggregates
			(project_id, service, operation, resource, kind, bucket, count, error_count, p50_us, p95_us, p99_us, max_us, sum_duration_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(project_id, service, operation, resource, kind, bucket)
			DO UPDATE SET
				count = count + excluded.count,
				error_count = error_count + excluded.error_count,
				p50_us = excluded.p50_us,
				p95_us = excluded.p95_us,
				p99_us = excluded.p99_us,
				max_us = MAX(max_us, excluded.max_us),
				sum_duration_us = sum_duration_us + excluded.sum_duration_us`,
			agg.ProjectID, agg.Service, agg.Operation, agg.Resource, agg.Kind,
			agg.Bucket, agg.Count, agg.ErrorCount,
			agg.P50Us, agg.P95Us, agg.P99Us, agg.MaxUs, agg.SumDurationUs,
		)
		return err
	})
}

func (r *Repository) UpsertAggregates(aggs []Aggregate) error {
	if len(aggs) == 0 {
		return nil
	}
	// Writing telemetry must not emit telemetry. See WithoutSpanTracing.
	ctx := WithoutSpanTracing(context.Background())
	return r.execLow(func() error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		stmt, err := tx.PrepareContext(ctx, `INSERT INTO aggregates
			(project_id, service, operation, resource, kind, bucket, count, error_count, p50_us, p95_us, p99_us, max_us, sum_duration_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(project_id, service, operation, resource, kind, bucket)
			DO UPDATE SET
				count = count + excluded.count,
				error_count = error_count + excluded.error_count,
				p50_us = excluded.p50_us,
				p95_us = excluded.p95_us,
				p99_us = excluded.p99_us,
				max_us = MAX(max_us, excluded.max_us),
				sum_duration_us = sum_duration_us + excluded.sum_duration_us`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, agg := range aggs {
			if _, err := stmt.ExecContext(ctx,
				agg.ProjectID, agg.Service, agg.Operation, agg.Resource, agg.Kind,
				agg.Bucket, agg.Count, agg.ErrorCount,
				agg.P50Us, agg.P95Us, agg.P99Us, agg.MaxUs, agg.SumDurationUs,
			); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
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

	q := "SELECT id, project_id, service, operation, resource, kind, bucket, count, error_count, p50_us, p95_us, p99_us, max_us, sum_duration_us FROM aggregates"
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
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) DeleteAggregatesOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	return r.batchedDelete(ctx, func() (int64, error) {
		res, e := r.db.ExecContext(ctx,
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
