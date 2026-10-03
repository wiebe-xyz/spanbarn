package repository

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (r *Repository) InsertSpans(spans []Span) error {
	return r.InsertSpansContext(context.Background(), spans)
}

func (r *Repository) InsertSpansContext(ctx context.Context, spans []Span) error {
	if len(spans) == 0 {
		return nil
	}
	// Writing spans must not emit spans. See WithoutSpanTracing.
	ctx = WithoutSpanTracing(ctx)
	return r.execLow(func() error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		stmt, err := tx.PrepareContext(ctx, `INSERT INTO spans
			(project_id, trace_id, span_id, parent_span_id, name, service, resource, kind, status, start_time_us, duration_us, attributes, events, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, s := range spans {
			var parentID *string
			if s.ParentSpanID != "" {
				parentID = &s.ParentSpanID
			}
			if _, err := stmt.ExecContext(ctx,
				s.ProjectID, s.TraceID, s.SpanID, parentID,
				s.Name, s.Service, s.Resource, s.Kind, s.Status,
				s.StartTimeUs, s.DurationUs, s.Attributes, s.Events, s.ExpiresAt,
			); err != nil {
				return err
			}
		}

		// Maintain trace_summaries in the same tx (staging-disabled path; the
		// staging flush does the same for the normal path).
		if sums := buildTraceSummaries(spans, time.Now().UTC()); len(sums) > 0 {
			if err := upsertTraceSummariesTx(ctx, tx, sums); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// appendCommonWhere appends the optional span filters shared by the span/trace
// query methods — project, service, operation, status, minimum duration and the
// ingested_at time range — as parameterised predicates. Method-specific filters
// (trace ID, operation exclusions) are added separately by the caller. All
// predicates are ANDed, so the append order does not affect results.
func (f SpanFilter) appendCommonWhere(where []string, args []any) ([]string, []any) {
	if f.ProjectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Service != "" {
		where = append(where, "service = ?")
		args = append(args, f.Service)
	}
	if f.Operation != "" {
		where = append(where, "name = ?")
		args = append(args, f.Operation)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.MinDuration > 0 {
		where = append(where, "duration_us >= ?")
		args = append(args, f.MinDuration)
	}
	if !f.From.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, f.To)
	}
	return where, args
}

func (r *Repository) QuerySpans(f SpanFilter) ([]Span, error) {
	f, err := r.withCalc(f)
	if err != nil {
		return nil, err
	}
	var where []string
	var args []any

	where, args = f.appendCommonWhere(where, args)
	if f.TraceID != "" {
		where = append(where, "trace_id = ?")
		args = append(args, f.TraceID)
	}
	where, args, err = f.appendExprWhere(where, args)
	if err != nil {
		return nil, err
	}

	q := "SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ingested_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	return r.scanSpans(q, args...)
}

func (r *Repository) GetTraceByID(traceID string) ([]Span, error) {
	return r.scanSpans(
		"SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans WHERE trace_id = ? ORDER BY start_time_us",
		traceID,
	)
}

func (r *Repository) GetSpansBySpanIDs(spanIDs []string) ([]Span, error) {
	if len(spanIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(spanIDs))
	args := make([]any, len(spanIDs))
	for i, id := range spanIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	return r.scanSpans(
		"SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans WHERE span_id IN ("+strings.Join(placeholders, ",")+")",
		args...,
	)
}

func (r *Repository) DeleteSpansByIDs(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var n int64
	err := r.execLow(func() error {
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		q := "DELETE FROM spans WHERE id IN (" + strings.Join(placeholders, ",") + ")"
		res, e := r.db.Exec(q, args...)
		if e != nil {
			return e
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

func (r *Repository) DeleteSpansByMaxID(maxID int64) (int64, error) {
	return r.execLowAffecting("DELETE FROM spans WHERE id <= ?", maxID)
}

func (r *Repository) DeleteBoringTraces(olderThan, newerThan time.Time, slowThresholdUS int64) (int64, error) {
	var total int64
	for {
		var n int64
		err := r.execLow(func() error {
			res, e := r.db.Exec(`DELETE FROM spans WHERE trace_id IN (
				SELECT trace_id FROM spans
				WHERE ingested_at < ? AND ingested_at >= ?
				GROUP BY trace_id
				HAVING MAX(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END) = 0
				AND MAX(duration_us) <= ?
				LIMIT 1000)`,
				olderThan, newerThan, slowThresholdUS)
			if e != nil {
				return e
			}
			n, _ = res.RowsAffected()
			return nil
		})
		if err != nil {
			return total, err
		}
		total += n
		if n == 0 {
			break
		}
	}
	return total, nil
}

func (r *Repository) DeleteSpansOlderThan(cutoff time.Time) (int64, error) {
	return r.execLowAffecting("DELETE FROM spans WHERE ingested_at < ?", cutoff)
}

// DeleteExpiredBoringSpans deletes sampled-boring spans whose stamped expires_at
// has passed. Classification stamps expires_at at storage time, so cleanup is a
// bounded seek of the partial idx_spans_expires index — no scan of the whole
// table fetching duration_us per row (which had grown into a 30s+ write-slot
// wedge). Interesting spans carry a NULL expires_at and are removed by the
// aggregate-then-delete pass instead; pre-migration rows are also NULL and drain
// that same way.
func (r *Repository) DeleteExpiredBoringSpans(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.UTC()
	return r.batchedDelete(ctx, func() (int64, error) {
		res, e := r.db.ExecContext(ctx,
			`DELETE FROM spans WHERE rowid IN (
				SELECT rowid FROM spans
				WHERE expires_at IS NOT NULL AND expires_at < ?
				LIMIT ?)`,
			cutoff, retentionDeleteBatch,
		)
		if e != nil {
			return 0, e
		}
		n, _ := res.RowsAffected()
		return n, nil
	})
}

func (r *Repository) CountSpansOlderThan(cutoff time.Time) (int64, error) {
	ctx, cancel := r.queryContext()
	defer cancel()
	var n int64
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM spans WHERE ingested_at <= ?", cutoff).Scan(&n)
	return n, err
}

func (r *Repository) GetSpansForAggregation(cutoff time.Time, limit int) ([]Span, error) {
	if limit <= 0 {
		limit = 1000
	}
	return r.scanSpans(
		"SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans WHERE ingested_at <= ? ORDER BY ingested_at LIMIT ?",
		cutoff, limit,
	)
}
