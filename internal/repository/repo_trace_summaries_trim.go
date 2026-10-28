package repository

import (
	"context"
	"database/sql"
	"time"
)

// The trace-summary delete reads a window of a project's summaries ingested
// before the later of its two cutoffs in idx_trace_summaries_list order, then
// deletes the window's deletable rows by rowid, like the error-sample delete.
//
// It used one DELETE per batch over a scan of the whole table in rowid order.
// Every batch walked the summaries retention keeps (error and durable traces
// younger than the error cutoff, exemplar traces, boring traces waiting for
// expires_at) from the oldest again, and on prod one batch held the write slot
// for 12s (2026-10-28).

// traceSummaryDeletableSQL is true for a summary of alias that retention
// drops: a plain trace ingested before the interesting cutoff ic, or an error
// or durable trace ingested before the error cutoff ec whose trace no error
// exemplar lists. Boring summaries go by expires_at in
// DeleteExpiredTraceSummaries.
func traceSummaryDeletableSQL(alias, ic, ec string) string {
	return alias + `.expires_at IS NULL
		  AND ((` + alias + `.has_error = 0 AND ` + alias + `.durable = 0 AND ` + alias + `.ingested_at < ` + ic + `)
		    OR ((` + alias + `.has_error = 1 OR ` + alias + `.durable = 1) AND ` + alias + `.ingested_at < ` + ec + `
		        AND NOT EXISTS (SELECT 1 FROM error_exemplars x
		                        WHERE x.project_id = ` + alias + `.project_id
		                          AND x.trace_id = ` + alias + `.trace_id)))`
}

// traceSummaryWindowSQL reads one window of a project's summaries ingested
// before ?2, each with whether retention keeps it. The index stores
// ingested_at descending, so walking it backwards yields ingested_at
// ascending and rowid descending within one time; the cursor clause follows
// that order. The last column is read only to load the row's primary key
// entry into the page cache.
func traceSummaryWindowSQL(resume bool) string {
	after := ""
	if resume {
		after = ` AND t.ingested_at >= ?3 AND (t.ingested_at > ?3 OR t.rowid < ?4)`
	}
	return `
		SELECT t.rowid, CAST(t.ingested_at AS TEXT),
		       NOT (` + traceSummaryDeletableSQL("t", "?6", "?7") + `),
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
var deleteTraceSummaryRowsSQL = `DELETE FROM trace_summaries
	WHERE rowid IN (SELECT value FROM json_each(?1))
	  AND project_id = ?2
	  AND ` + traceSummaryDeletableSQL("trace_summaries", "?3", "?4")

// traceSummaryDeleteLabel keeps the label the batched delete had, so slow
// writes before and after the change group under one op.
var traceSummaryDeleteLabel = "DeleteTraceSummariesOlderThan:" + FamilySpans.String()

// traceSummaryDelete deletes one project's summaries past their cutoff.
func traceSummaryDelete(ctx context.Context, pid int64, ic, ec time.Time) windowDelete {
	upper := ic
	if ec.After(upper) {
		upper = ec
	}
	return windowDelete{
		family: FamilySpans,
		label:  traceSummaryDeleteLabel,
		scan: func(q rowQuerier, cur logTrimCursor) (logTrimWindow, error) {
			return scanWindow(ctx, q, traceSummaryWindowSQL(cur.set),
				pid, upper, cur.at, cur.rowid, retentionDeleteBatch, ic, ec)
		},
		del: func(db *sql.DB, ids []int64) (int64, error) {
			return deleteRowsByID(ctx, db, deleteTraceSummaryRowsSQL, ids, pid, ic, ec)
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
// Each project resumes from the cursor the previous pass left, so the kept
// summaries a pass has walked are not read again. A kept summary behind the
// cursor becomes deletable when a cutoff passes it, when its trace loses
// expires_at or when PruneErrorExemplars drops its exemplar; the cursor starts
// over after logTrimCursorMaxAge, and PruneErrorExemplars resets it.
func (r *Repository) DeleteTraceSummariesOlderThan(ctx context.Context, interestingCutoff, errorCutoff time.Time) (int64, error) {
	ic, ec := interestingCutoff.UTC(), errorCutoff.UTC()
	return r.deleteByProject(ctx, "trace_summaries", r.traceSummaryTrim, func(pid int64) windowDelete {
		return traceSummaryDelete(ctx, pid, ic, ec)
	})
}
