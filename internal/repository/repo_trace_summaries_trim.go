package repository

import (
	"context"
	"database/sql"
	"time"
)

// The trace-summary delete reads windows of a project's summaries in
// idx_trace_summaries_list order and deletes each window's deletable rows by
// rowid, like the error-sample delete. It walks twice, each walk with its own
// cursor and up to its own cutoff: plain traces up to the interesting cutoff,
// error and durable traces up to the error cutoff. A summary one walk keeps
// for its kind stays out of that walk for good, so a moving cutoff only ever
// reaches rows ahead of the cursor. One walk to the later cutoff would pass
// error traces younger than the error cutoff and miss them once the cutoff
// moved past them.
//
// It used one DELETE per batch over a scan of the whole table in rowid order.
// Every batch walked the summaries retention keeps (error and durable traces
// younger than the error cutoff, exemplar traces, boring traces waiting for
// expires_at) from the oldest again, and on prod one batch held the write slot
// for 12s (2026-10-28).

// traceSummaryPlainSQL is true for a summary of alias that the plain walk
// drops once the interesting cutoff passes it: no error, not durable, no
// expires_at. Boring summaries go by expires_at in DeleteExpiredTraceSummaries.
func traceSummaryPlainSQL(alias string) string {
	return alias + `.expires_at IS NULL AND ` + alias + `.has_error = 0 AND ` + alias + `.durable = 0`
}

// traceSummaryErrorSQL is true for a summary of alias that the error walk
// drops once the error cutoff passes it: an error or durable trace whose
// trace no error exemplar lists.
func traceSummaryErrorSQL(alias string) string {
	return alias + `.expires_at IS NULL AND (` + alias + `.has_error = 1 OR ` + alias + `.durable = 1)
		  AND NOT EXISTS (SELECT 1 FROM error_exemplars x
		                  WHERE x.project_id = ` + alias + `.project_id
		                    AND x.trace_id = ` + alias + `.trace_id)`
}

// traceSummaryWindowSQL reads one window of a project's summaries ingested
// before ?2, each with whether the walk keeps it. The index stores
// ingested_at descending, so walking it backwards yields ingested_at
// ascending and rowid descending within one time; the cursor clause follows
// that order. The last column is read only to load the row's primary key
// entry into the page cache.
func traceSummaryWindowSQL(deletable func(alias string) string, resume bool) string {
	after := ""
	if resume {
		after = ` AND t.ingested_at >= ?3 AND (t.ingested_at > ?3 OR t.rowid < ?4)`
	}
	return `
		SELECT t.rowid, CAST(t.ingested_at AS TEXT),
		       NOT (` + deletable("t") + `),
		       EXISTS (SELECT 1 FROM trace_summaries p
		               WHERE p.project_id = t.project_id AND p.trace_id = t.trace_id)
		FROM trace_summaries t INDEXED BY idx_trace_summaries_list
		WHERE t.project_id = ?1 AND t.ingested_at < ?2` + after + `
		ORDER BY t.ingested_at, t.rowid DESC
		LIMIT ?5`
}

// deleteTraceSummaryRowsSQL deletes the listed rows and checks each one again:
// when the window was read outside the write slot, a trace can have gained an
// error, a durable span or an exemplar since, and a row id can have been
// reused by a newer row.
func deleteTraceSummaryRowsSQL(deletable func(alias string) string) string {
	return `DELETE FROM trace_summaries
	WHERE rowid IN (SELECT value FROM json_each(?1))
	  AND project_id = ?2 AND ingested_at < ?3
	  AND ` + deletable("trace_summaries")
}

// traceSummaryDeleteLabel keeps the label the batched delete had, so slow
// writes before and after the change group under one op.
var traceSummaryDeleteLabel = "DeleteTraceSummariesOlderThan:" + FamilySpans.String()

// traceSummaryDelete deletes one project's summaries that deletable selects
// and that were ingested before cutoff.
func traceSummaryDelete(ctx context.Context, pid int64, deletable func(alias string) string, cutoff time.Time) windowDelete {
	return windowDelete{
		family: FamilySpans,
		label:  traceSummaryDeleteLabel,
		scan: func(q rowQuerier, cur logTrimCursor) (logTrimWindow, error) {
			return scanWindow(ctx, q, traceSummaryWindowSQL(deletable, cur.set),
				pid, cutoff, cur.at, cur.rowid, retentionDeleteBatch)
		},
		del: func(db *sql.DB, ids []int64) (int64, error) {
			return deleteRowsByID(ctx, db, deleteTraceSummaryRowsSQL(deletable), ids, pid, cutoff)
		},
	}
}

// DeleteTraceSummariesOlderThan drops indefinitely-kept summaries once their
// spans are gone: plain traces (no error, not durable) at interestingCutoff
// (matching the span aggregate-then-delete pass), and error or durable traces
// at errorCutoff. Retention copies error and durable spans to error_samples,
// so those traces keep listing for exactly as long as their spans can be read.
// An error exemplar's summary stays with its error samples, until
// PruneErrorExemplars releases the trace.
//
// Each walk resumes from the cursor the previous pass left, so the kept
// summaries a pass has walked are not read again. A summary behind a cursor
// becomes deletable for that walk only when its trace loses expires_at, when
// a plain trace gains an error, or when PruneErrorExemplars drops its
// exemplar; the cursors start over after logTrimCursorMaxAge, and
// PruneErrorExemplars resets the error walk's.
func (r *Repository) DeleteTraceSummariesOlderThan(ctx context.Context, interestingCutoff, errorCutoff time.Time) (int64, error) {
	ic, ec := interestingCutoff.UTC(), errorCutoff.UTC()
	plain, err := r.deleteByProject(ctx, "trace_summaries", r.traceSummaryTrim, func(pid int64) windowDelete {
		return traceSummaryDelete(ctx, pid, traceSummaryPlainSQL, ic)
	})
	if err != nil {
		return plain, err
	}
	errs, err := r.deleteByProject(ctx, "trace_summaries", r.traceSummaryErrorTrim, func(pid int64) windowDelete {
		return traceSummaryDelete(ctx, pid, traceSummaryErrorSQL, ec)
	})
	return plain + errs, err
}
