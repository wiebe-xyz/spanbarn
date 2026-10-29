package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// stagingCols is the column list shared by staging reads/writes (matches spans,
// minus the auto id). ingested_at is preserved across the staging->spans move so
// a span's received time doesn't jump to flush time.
const stagingCols = `project_id, trace_id, span_id, parent_span_id, name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at, sample_weight`

// sampleWeightArg stores an unknown weight (0) as NULL, so the column reads the
// same for a span the trace buffer never weighted and a row from before the
// column existed.
func sampleWeightArg(w int) any {
	if w <= 0 {
		return nil
	}
	return w
}

// InsertSpansStaging appends spans to the unindexed-ish spans_staging landing
// table (one trace_id index). This is the cheap write that lets the writer drain
// the Redis queue fast; a background flush later classifies and moves the
// interesting traces into the indexed spans table.
func (r *Repository) InsertSpansStaging(ctx context.Context, spans []Span) error {
	if len(spans) == 0 {
		return nil
	}
	// Writing spans must not emit spans. See WithoutSpanTracing.
	ctx = WithoutSpanTracing(ctx)
	return r.execLow(FamilySpans, func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		stmt, err := tx.PrepareContext(ctx, `INSERT INTO spans_staging
			(project_id, trace_id, span_id, parent_span_id, name, service, resource, kind, status, start_time_us, duration_us, attributes, events, sample_weight)
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
				s.StartTimeUs, s.DurationUs, s.Attributes, s.Events, sampleWeightArg(s.SampleWeight),
			); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// ReadyStagingTraceIDs returns up to limit trace_ids whose oldest staged span is
// older than cutoff (i.e. the trace has had the full buffering window to arrive),
// oldest first. The GROUP BY is served by the trace_id index and the table stays
// small (continuously drained), so this is cheap.
func (r *Repository) ReadyStagingTraceIDs(ctx context.Context, cutoff time.Time, limit int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT trace_id FROM spans_staging
		 GROUP BY trace_id HAVING MIN(ingested_at) < ?
		 ORDER BY MIN(ingested_at) LIMIT ?`, cutoff.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GetStagingSpansByTraceIDs returns every staged span for the given traces, so
// classification sees the whole trace. Uses the trace_id index.
func (r *Repository) GetStagingSpansByTraceIDs(ctx context.Context, traceIDs []string) ([]Span, error) {
	if len(traceIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(traceIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(traceIDs))
	for i, id := range traceIDs {
		args[i] = id
	}
	q := `SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at, COALESCE(sample_weight, 0)
		FROM spans_staging WHERE trace_id IN (` + placeholders + `)`
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Span
	for rows.Next() {
		var s Span
		if err := rows.Scan(
			&s.ID, &s.ProjectID, &s.TraceID, &s.SpanID, &s.ParentSpanID,
			&s.Name, &s.Service, &s.Resource, &s.Kind, &s.Status,
			&s.StartTimeUs, &s.DurationUs, &s.Attributes, &s.Events, &s.IngestedAt, &s.SampleWeight,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CommitStagingFlush atomically moves the interesting spans into the indexed
// spans table (preserving ingested_at) and deletes every staged row for the
// given traces. Boring traces have no interesting spans, so they are dropped by
// the delete without ever hitting the indexed table.
func (r *Repository) CommitStagingFlush(ctx context.Context, traceIDs []string, interesting []Span) error {
	if len(traceIDs) == 0 {
		return nil
	}
	_, err := r.execLowRows(FamilySpans, func(db *sql.DB) (int64, error) {
		n := int64(len(interesting))
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return n, err
		}
		defer tx.Rollback()

		if err := insertStagedSpansTx(ctx, tx, interesting); err != nil {
			return n, err
		}
		// Roll up the persisted spans into trace_summaries in the same tx so the
		// trace list has a pre-grouped, indexed row instead of scanning spans.
		if sums := buildTraceSummaries(interesting, time.Now().UTC()); len(sums) > 0 {
			if err := upsertTraceSummariesTx(ctx, tx, sums); err != nil {
				return n, err
			}
		}

		placeholders := strings.Repeat("?,", len(traceIDs))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(traceIDs))
		for i, id := range traceIDs {
			args[i] = id
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM spans_staging WHERE trace_id IN (`+placeholders+`)`, args...)
		if err != nil {
			return n, err
		}
		staged, _ := res.RowsAffected()
		n += staged
		return n, tx.Commit()
	})
	return err
}

// CommitStagingTracePart moves one part of a trace too large for one job: the
// part's interesting spans go into the indexed spans table and its staged rows,
// named by staging id, leave spans_staging, in one transaction. The trace
// summary adds the part's counts. refresh recomputes the trace's structure from
// its stored spans; the trace's last part sets it, so the structure is read
// once, over every part.
func (r *Repository) CommitStagingTracePart(ctx context.Context, key TraceKey, stagedIDs []int64, interesting []Span, refresh bool) error {
	if len(stagedIDs) == 0 {
		return nil
	}
	ids, err := json.Marshal(stagedIDs)
	if err != nil {
		return err
	}
	_, err = r.execLowRows(FamilySpans, func(db *sql.DB) (int64, error) {
		n := int64(len(interesting))
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return n, err
		}
		defer tx.Rollback()

		if err := insertStagedSpansTx(ctx, tx, interesting); err != nil {
			return n, err
		}
		if err := upsertTraceSummaryRowsTx(ctx, tx, buildTraceSummaries(interesting, time.Now().UTC())); err != nil {
			return n, err
		}
		if refresh {
			if err := refreshTraceStructureTx(ctx, tx, []TraceKey{key}); err != nil {
				return n, err
			}
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM spans_staging WHERE id IN (SELECT value FROM json_each(?)) AND trace_id = ?`,
			string(ids), key.TraceID)
		if err != nil {
			return n, err
		}
		staged, _ := res.RowsAffected()
		n += staged
		return n, tx.Commit()
	})
	return err
}

// insertStagedSpansTx inserts staged spans into the indexed spans table,
// keeping their ingested_at.
func insertStagedSpansTx(ctx context.Context, tx *sql.Tx, spans []Span) error {
	if len(spans) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO spans
		(`+stagingCols+`, expires_at, durable)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
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
			s.StartTimeUs, s.DurationUs, s.Attributes, s.Events, s.IngestedAt, sampleWeightArg(s.SampleWeight),
			s.ExpiresAt, s.Durable,
		); err != nil {
			return err
		}
	}
	return nil
}

// DeleteStagingOlderThan is the hard cleanup backstop: it drops any staged row
// older than cutoff regardless of whether the flush processed it, guaranteeing
// spans_staging can never grow without bound if the flush stalls. It sheds the
// oldest rows (graceful degradation) rather than letting the table grow.
func (r *Repository) DeleteStagingOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	// Batched so a large backlog (e.g. rows accumulated across a restart) is never
	// deleted in one statement that holds the single write connection for tens of
	// seconds and wedges the writer. Rows are inserted in ~ingested_at order
	// (monotonic rowid), so each LIMIT batch's scan hits the oldest rows first and
	// returns fast even without a standalone ingested_at index on staging.
	c := cutoff.UTC()
	return r.batchedDelete(ctx, FamilySpans, func(db *sql.DB) (int64, error) {
		res, e := db.ExecContext(ctx,
			`DELETE FROM spans_staging WHERE rowid IN (SELECT rowid FROM spans_staging WHERE ingested_at < ? LIMIT ?)`,
			c, retentionDeleteBatch)
		if e != nil {
			return 0, e
		}
		n, _ := res.RowsAffected()
		return n, nil
	})
}

// CountStagingRows returns the current staging depth for observability.
func (r *Repository) CountStagingRows(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM spans_staging`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count spans_staging: %w", err)
	}
	return n, nil
}
