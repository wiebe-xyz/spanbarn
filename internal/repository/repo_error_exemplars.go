package repository

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// exemplarDayLayout is the format of error_exemplars.day.
const exemplarDayLayout = "2006-01-02"

// recordErrorExemplars lists, for every error span in spans, its trace as the
// exemplar of (project, service, operation, UTC day of the span's start)
// unless that day already has one. It runs in the transaction that copies the
// spans to error_samples, so an exemplar never names a trace whose samples
// were not written. now dates a span that carries no start time.
func recordErrorExemplars(tx *sql.Tx, spans []Span, now time.Time) error {
	var stmt *sql.Stmt
	for _, s := range spans {
		if !strings.EqualFold(s.Status, "error") {
			continue
		}
		if stmt == nil {
			var err error
			stmt, err = tx.Prepare(`INSERT OR IGNORE INTO error_exemplars
				(project_id, service, name, day, trace_id) VALUES (?, ?, ?, ?, ?)`)
			if err != nil {
				return err
			}
			defer stmt.Close()
		}
		if _, err := stmt.Exec(s.ProjectID, s.Service, s.Name, exemplarDay(s, now), s.TraceID); err != nil {
			return err
		}
	}
	return nil
}

func exemplarDay(s Span, now time.Time) string {
	t := now
	if s.StartTimeUs > 0 {
		t = time.UnixMicro(s.StartTimeUs)
	}
	return t.UTC().Format(exemplarDayLayout)
}

// PruneErrorExemplars drops exemplar rows for days before cutoff's UTC day.
// The traces they named then fall to the normal error cutoff: the next
// retention cycle deletes their error samples and summaries if those are past
// it.
func (r *Repository) PruneErrorExemplars(ctx context.Context, cutoff time.Time) (int64, error) {
	day := cutoff.UTC().Format(exemplarDayLayout)
	return r.batchedDelete(ctx, FamilySpans, func(db *sql.DB) (int64, error) {
		res, err := db.ExecContext(ctx,
			`DELETE FROM error_exemplars WHERE rowid IN (
				SELECT rowid FROM error_exemplars WHERE day < ? LIMIT ?)`,
			day, retentionDeleteBatch)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return n, nil
	})
}
