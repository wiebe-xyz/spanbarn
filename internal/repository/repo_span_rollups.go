package repository

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
)

// SpanRollup is one row of span_rollups: the spans of one (project, service,
// name, kind, root or not, status, HTTP status) that arrived in one bucket.
type SpanRollup struct {
	StepSeconds int64
	Bucket      time.Time // bucket start, whole seconds, UTC
	ProjectID   int64
	Service     string
	Name        string
	Kind        string
	IsRoot      bool
	Status      string
	// HTTPStatus is the response code of a root span; 0 for a span without
	// one and for every child span.
	HTTPStatus int64
	Count      int64
	// WeightedCount sums the sample weights of the spans that had one and
	// WeightedSpans counts those spans, as on aggregates (see SpanWeights).
	WeightedCount int64
	WeightedSpans int64
	SumDurationUs int64
	MinUs         int64
	MaxUs         int64
	Hist          []byte // spanhist encoding, every span counted once
}

// spanRollupChunk caps the rows one span rollup write holds in one write
// slot, as metricRollupChunk does for metric rollups.
const spanRollupChunk = 500

const spanRollupColumns = `step_seconds, bucket, project_id, service, name, kind, is_root, status, http_status,
	count, weighted_count, weighted_spans, sum_duration_us, min_us, max_us, hist`

// spanRollupAddSQL folds a flush into its row: counts and sums add, min and
// max take the extreme and the histograms merge, so the accumulator can write
// the open bucket on every flush and a late span still lands in its bucket.
const spanRollupAddSQL = `INSERT INTO span_rollups (` + spanRollupColumns + `)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(step_seconds, bucket, project_id, service, name, kind, is_root, status, http_status)
	DO UPDATE SET
		count = count + excluded.count,
		weighted_count = weighted_count + excluded.weighted_count,
		weighted_spans = weighted_spans + excluded.weighted_spans,
		sum_duration_us = sum_duration_us + excluded.sum_duration_us,
		min_us = MIN(min_us, excluded.min_us),
		max_us = MAX(max_us, excluded.max_us),
		hist = ` + spanHistMergeFunc + `(hist, excluded.hist)`

// spanRollupReplaceSQL writes a compacted row whole. Compaction builds each
// coarse row from every source row of its bucket, so writing it again after an
// interrupted pass replaces it with the same value instead of counting twice.
const spanRollupReplaceSQL = `INSERT OR REPLACE INTO span_rollups (` + spanRollupColumns + `)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func spanRollupArgs(s SpanRollup) []any {
	root := 0
	if s.IsRoot {
		root = 1
	}
	return []any{
		s.StepSeconds, s.Bucket.Unix(), s.ProjectID, s.Service, s.Name, s.Kind, root, s.Status, s.HTTPStatus,
		s.Count, s.WeightedCount, s.WeightedSpans, s.SumDurationUs, s.MinUs, s.MaxUs, s.Hist,
	}
}

// AddSpanRollups folds a flush of span rollups into their rows, in key order,
// one transaction and one write slot per chunk of spanRollupChunk rows. A
// chunk that fails stops the batch; the chunks before it stay written.
func (r *Repository) AddSpanRollups(rows []SpanRollup) error {
	// Writing telemetry must not emit telemetry. See WithoutSpanTracing.
	ctx := WithoutSpanTracing(context.Background())
	for _, chunk := range spanRollupChunks(rows) {
		if err := r.execLow(FamilySpans, func(db *sql.DB) error {
			return writeSpanRollupChunk(ctx, db, spanRollupAddSQL, chunk)
		}); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceSpanRollups writes compacted rows whole, chunked like AddSpanRollups.
func (r *Repository) ReplaceSpanRollups(ctx context.Context, rows []SpanRollup) error {
	ctx = WithoutSpanTracing(ctx)
	for _, chunk := range spanRollupChunks(rows) {
		if err := r.execLow(FamilySpans, func(db *sql.DB) error {
			return writeSpanRollupChunk(ctx, db, spanRollupReplaceSQL, chunk)
		}); err != nil {
			return err
		}
	}
	return nil
}

// spanRollupChunks sorts a copy of rows by primary key, so each chunk writes
// neighbouring leaves, and splits it into chunks.
func spanRollupChunks(rows []SpanRollup) [][]SpanRollup {
	sorted := slices.Clone(rows)
	slices.SortFunc(sorted, compareSpanRollupKey)
	return slices.Collect(slices.Chunk(sorted, spanRollupChunk))
}

func compareSpanRollupKey(a, b SpanRollup) int {
	return cmp.Or(
		cmp.Compare(a.StepSeconds, b.StepSeconds),
		a.Bucket.Compare(b.Bucket),
		cmp.Compare(a.ProjectID, b.ProjectID),
		strings.Compare(a.Service, b.Service),
		strings.Compare(a.Name, b.Name),
		strings.Compare(a.Kind, b.Kind),
		compareBool(a.IsRoot, b.IsRoot),
		strings.Compare(a.Status, b.Status),
		cmp.Compare(a.HTTPStatus, b.HTTPStatus),
	)
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

func writeSpanRollupChunk(ctx context.Context, db *sql.DB, query string, chunk []SpanRollup) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, s := range chunk {
		if _, err := stmt.ExecContext(ctx, spanRollupArgs(s)...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SpanRollupWindow returns the rows of one tier with buckets in [from, to),
// a slice of the primary key.
func (r *Repository) SpanRollupWindow(ctx context.Context, step int64, from, to time.Time) ([]SpanRollup, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	rows, err := r.db.QueryContext(ctx, `SELECT `+spanRollupColumns+` FROM span_rollups
		WHERE step_seconds = ? AND bucket >= ? AND bucket < ?`, step, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpanRollup
	for rows.Next() {
		var s SpanRollup
		var bucket int64
		if err := rows.Scan(&s.StepSeconds, &bucket, &s.ProjectID, &s.Service, &s.Name, &s.Kind, &s.IsRoot, &s.Status, &s.HTTPStatus,
			&s.Count, &s.WeightedCount, &s.WeightedSpans, &s.SumDurationUs, &s.MinUs, &s.MaxUs, &s.Hist); err != nil {
			return nil, err
		}
		s.Bucket = time.Unix(bucket, 0).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// OldestSpanRollupBucket returns the earliest bucket of one tier, read from
// the front of the primary key.
func (r *Repository) OldestSpanRollupBucket(ctx context.Context, step int64) (time.Time, bool, error) {
	var bucket int64
	err := r.db.QueryRowContext(ctx,
		`SELECT bucket FROM span_rollups WHERE step_seconds = ? ORDER BY bucket LIMIT 1`, step).Scan(&bucket)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(bucket, 0).UTC(), true, nil
}

// SpanRollupWatermark reports how far a tier has been compacted into the tier
// above it. Retention deletes a tier only up to its watermark.
func (r *Repository) SpanRollupWatermark(ctx context.Context, step int64) (time.Time, bool, error) {
	var through int64
	err := r.db.QueryRowContext(ctx,
		`SELECT compacted_through FROM span_rollup_compaction WHERE step_seconds = ?`, step).Scan(&through)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(through, 0).UTC(), true, nil
}

// SetSpanRollupWatermark advances a tier's watermark. It never moves back.
func (r *Repository) SetSpanRollupWatermark(ctx context.Context, step int64, through time.Time) error {
	ctx = WithoutSpanTracing(ctx)
	return r.execLow(FamilySpans, func(db *sql.DB) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO span_rollup_compaction (step_seconds, compacted_through) VALUES (?, ?)
			 ON CONFLICT(step_seconds) DO UPDATE SET
				compacted_through = MAX(compacted_through, excluded.compacted_through)`,
			step, through.Unix())
		return err
	})
}

// DeleteSpanRollupsOlderThanLimited removes at most max rows of one tier with
// buckets before cutoff, oldest first, reporting whether more remain. Each
// batch walks the front of the primary key.
func (r *Repository) DeleteSpanRollupsOlderThanLimited(ctx context.Context, step int64, cutoff time.Time, max int64) (int64, bool, error) {
	const q = `DELETE FROM span_rollups
		WHERE (step_seconds, bucket, project_id, service, name, kind, is_root, status) IN (
			SELECT step_seconds, bucket, project_id, service, name, kind, is_root, status
			FROM span_rollups WHERE step_seconds = ? AND bucket < ? LIMIT ?)`
	return r.batchedDeleteLimited(ctx, FamilySpans, max, func(db *sql.DB, limit int64) (int64, error) {
		res, err := db.ExecContext(ctx, q, step, cutoff.Unix(), limit)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return n, nil
	})
}
