package repository

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// traceSummaryAgg is the per-trace rollup upserted into trace_summaries. It is
// derived from the spans being persisted for a trace (see buildTraceSummaries).
type traceSummaryAgg struct {
	projectID   int64
	traceID     string
	rootName    string // "" when this batch holds no true root span (parent == "")
	rootService string
	rootDurUs   int64
	startTimeUs int64
	spanCount   int64
	hasError    bool
	ingestedAt  time.Time
	expiresAt   *time.Time // nil when any span in the trace is kept indefinitely (interesting)
}

// buildTraceSummaries reduces a batch of persisted spans into one rollup per
// (project_id, trace_id). now supplies ingested_at for spans that don't carry
// one yet (the inline insert path lets the DB default it). The rollup mirrors the
// span-list semantics: min start time, total span count, has_error = any error,
// root fields from the true root span, and an expires_at that is nil unless every
// span in the trace is a sampled-boring span (so the summary outlives its spans
// no longer than the spans themselves).
func buildTraceSummaries(spans []Span, now time.Time) []traceSummaryAgg {
	if len(spans) == 0 {
		return nil
	}
	type key struct {
		projectID int64
		traceID   string
	}
	byTrace := make(map[key]*traceSummaryAgg, len(spans))
	order := make([]key, 0, len(spans))

	for i := range spans {
		s := &spans[i]
		k := key{s.ProjectID, s.TraceID}
		ing := s.IngestedAt
		if ing.IsZero() {
			ing = now
		}
		agg, ok := byTrace[k]
		if !ok {
			agg = &traceSummaryAgg{
				projectID:   s.ProjectID,
				traceID:     s.TraceID,
				startTimeUs: s.StartTimeUs,
				ingestedAt:  ing,
				expiresAt:   s.ExpiresAt,
			}
			byTrace[k] = agg
			order = append(order, k)
		}
		agg.spanCount++
		if s.StartTimeUs < agg.startTimeUs {
			agg.startTimeUs = s.StartTimeUs
		}
		if ing.Before(agg.ingestedAt) {
			agg.ingestedAt = ing
		}
		if strings.EqualFold(s.Status, "error") {
			agg.hasError = true
		}
		// expires_at: nil wins (an indefinitely-kept span means the whole trace is
		// kept), otherwise take the latest expiry so the summary lives as long as
		// its last span.
		agg.expiresAt = mergeExpiry(agg.expiresAt, s.ExpiresAt, ok)
		// Root fields come only from the true root span (no parent). Leaving them
		// empty when this batch has no root lets a later upsert keep an
		// already-recorded root instead of clobbering it with a child span.
		if s.ParentSpanID == "" {
			agg.rootName = s.Name
			agg.rootService = s.Service
			agg.rootDurUs = s.DurationUs
		}
	}

	out := make([]traceSummaryAgg, 0, len(order))
	for _, k := range order {
		out = append(out, *byTrace[k])
	}
	return out
}

// mergeExpiry combines the accumulated expiry with a span's expiry. seen reports
// whether the accumulator already held a span (so the first span's value was
// taken verbatim by the caller and must not be re-merged). A nil expiry always
// wins — it marks a span kept until the interesting/error cutoff rather than the
// short boring window.
func mergeExpiry(acc, span *time.Time, seen bool) *time.Time {
	if !seen {
		return acc // first span: caller already assigned span's expiry
	}
	if acc == nil || span == nil {
		return nil
	}
	if span.After(*acc) {
		return span
	}
	return acc
}

// upsertTraceSummariesTx writes the rollups within an existing transaction, so
// they land atomically with the spans that produced them. Accumulating columns
// use MIN/SUM/MAX; root fields only overwrite when the incoming batch actually
// carried a root (root_name != ”).
func upsertTraceSummariesTx(ctx context.Context, tx *sql.Tx, sums []traceSummaryAgg) error {
	if len(sums) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO trace_summaries
		(project_id, trace_id, root_name, root_service, root_duration_us, start_time_us, span_count, has_error, ingested_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, trace_id) DO UPDATE SET
			start_time_us    = MIN(start_time_us, excluded.start_time_us),
			span_count       = span_count + excluded.span_count,
			has_error        = MAX(has_error, excluded.has_error),
			ingested_at      = MIN(ingested_at, excluded.ingested_at),
			root_name        = CASE WHEN excluded.root_name != '' THEN excluded.root_name        ELSE root_name        END,
			root_service     = CASE WHEN excluded.root_name != '' THEN excluded.root_service     ELSE root_service     END,
			root_duration_us = CASE WHEN excluded.root_name != '' THEN excluded.root_duration_us ELSE root_duration_us END,
			expires_at       = CASE WHEN expires_at IS NULL OR excluded.expires_at IS NULL THEN NULL
			                        ELSE MAX(expires_at, excluded.expires_at) END`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, s := range sums {
		hasErr := 0
		if s.hasError {
			hasErr = 1
		}
		if _, err := stmt.ExecContext(ctx,
			s.projectID, s.traceID, s.rootName, s.rootService, s.rootDurUs,
			s.startTimeUs, s.spanCount, hasErr, s.ingestedAt, s.expiresAt,
		); err != nil {
			return err
		}
	}
	return nil
}

// DeleteExpiredTraceSummaries drops summaries whose stamped expires_at has passed
// (boring-sampled traces), mirroring DeleteExpiredBoringSpans. Batched so a large
// backlog never holds the write connection for long.
func (r *Repository) DeleteExpiredTraceSummaries(ctx context.Context, now time.Time) (int64, error) {
	n := now.UTC()
	return r.batchedDelete(ctx, func() (int64, error) {
		res, e := r.db.ExecContext(ctx,
			`DELETE FROM trace_summaries WHERE rowid IN (
				SELECT rowid FROM trace_summaries WHERE expires_at IS NOT NULL AND expires_at < ? LIMIT ?)`,
			n, retentionDeleteBatch)
		if e != nil {
			return 0, e
		}
		c, _ := res.RowsAffected()
		return c, nil
	})
}

// DeleteTraceSummariesOlderThan drops indefinitely-kept summaries once their
// spans are gone: non-error traces at interestingCutoff (matching the span
// aggregate-then-delete pass) and error traces at errorCutoff (matching
// error_samples retention, so errors keep listing for exactly as long).
func (r *Repository) DeleteTraceSummariesOlderThan(ctx context.Context, interestingCutoff, errorCutoff time.Time) (int64, error) {
	ic, ec := interestingCutoff.UTC(), errorCutoff.UTC()
	return r.batchedDelete(ctx, func() (int64, error) {
		res, e := r.db.ExecContext(ctx,
			`DELETE FROM trace_summaries WHERE rowid IN (
				SELECT rowid FROM trace_summaries
				WHERE expires_at IS NULL
				  AND ((has_error = 0 AND ingested_at < ?) OR (has_error = 1 AND ingested_at < ?))
				LIMIT ?)`,
			ic, ec, retentionDeleteBatch)
		if e != nil {
			return 0, e
		}
		c, _ := res.RowsAffected()
		return c, nil
	})
}
