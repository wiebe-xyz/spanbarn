package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// spanLedgerRetention is how long the span ledger keeps a bucket. A bucket more
// than a day in the future comes from a client with a wrong clock and goes in
// the same pass.
const (
	spanLedgerRetention = 14 * 24 * time.Hour
	spanLedgerFutureMax = 24 * time.Hour
)

// AddSpanLedgerRows adds rows to the stored counts. It writes all of them or
// none, so a caller can retry a failed call without counting anything twice.
func (r *Repository) AddSpanLedgerRows(rows []model.SpanLedgerRow) error {
	if len(rows) == 0 {
		return nil
	}
	return r.execLow(FamilyCore, func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		for i := range rows {
			if err := upsertSpanLedgerRow(tx, &rows[i]); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func upsertSpanLedgerRow(tx *sql.Tx, row *model.SpanLedgerRow) error {
	_, err := tx.Exec(`INSERT INTO span_ledger
			(bucket, project_id, accepted, sampled_out, shed_unsampled, lost_in_buffer, forwarded, processed, failed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (bucket, project_id) DO UPDATE SET
			accepted       = accepted + excluded.accepted,
			sampled_out    = sampled_out + excluded.sampled_out,
			shed_unsampled = shed_unsampled + excluded.shed_unsampled,
			lost_in_buffer = lost_in_buffer + excluded.lost_in_buffer,
			forwarded      = forwarded + excluded.forwarded,
			processed      = processed + excluded.processed,
			failed         = failed + excluded.failed`,
		row.Bucket, row.ProjectID, row.Accepted, row.SampledOut, row.ShedUnsampled,
		row.LostInBuffer, row.Forwarded, row.Processed, row.Failed)
	return err
}

// ListSpanLedger returns the rows of the buckets from the one holding from up to
// to, oldest first, for one project or for all of them when projectID is 0.
func (r *Repository) ListSpanLedger(ctx context.Context, projectID int64, from, to time.Time) ([]model.SpanLedgerRow, error) {
	ctx, cancel := r.readContext(ctx)
	defer cancel()
	query := `SELECT bucket, project_id, accepted, sampled_out, shed_unsampled, lost_in_buffer, forwarded, processed, failed
		FROM span_ledger WHERE bucket >= ? AND bucket < ?`
	args := []any{model.SpanLedgerBucket(from.UnixMicro()), to.Unix()}
	if projectID > 0 {
		query += " AND project_id = ?"
		args = append(args, projectID)
	}
	rows, err := r.db.QueryContext(ctx, query+" ORDER BY bucket, project_id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SpanLedgerRow
	for rows.Next() {
		var row model.SpanLedgerRow
		if err := rows.Scan(&row.Bucket, &row.ProjectID, &row.Accepted, &row.SampledOut, &row.ShedUnsampled,
			&row.LostInBuffer, &row.Forwarded, &row.Processed, &row.Failed); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeleteExpiredSpanLedger removes the buckets older than the ledger keeps and
// those too far in the future to come from a right clock.
func (r *Repository) DeleteExpiredSpanLedger(now time.Time) (int64, error) {
	return r.execLowAffecting(FamilyCore,
		`DELETE FROM span_ledger WHERE bucket < ? OR bucket > ?`,
		now.Add(-spanLedgerRetention).Unix(), now.Add(spanLedgerFutureMax).Unix())
}
