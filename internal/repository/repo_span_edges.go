package repository

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// SpanEdge is one row of span_edges: the calls one service made to one
// target that arrived in one bucket. A row with an empty Target is the
// bucket's marker (see IsMarker).
type SpanEdge struct {
	StepSeconds int64
	Bucket      time.Time // bucket start, whole seconds, UTC
	ProjectID   int64
	// Source is the calling service; empty for a client span without one.
	Source string
	Target string
	// TargetType is "service" for a call into another instrumented service,
	// or the kind of dependency a client span named.
	TargetType string
	Count      int64
	ErrorCount int64
	// WeightedCount and WeightedSpans are the sample weights of the spans
	// that carry the calls, as on span_rollups.
	WeightedCount int64
	WeightedSpans int64
	SumDurationUs int64
	MinUs         int64
	MaxUs         int64
	Hist          []byte // spanhist encoding of the call durations
}

// IsMarker reports whether e is a bucket marker: the row every flushed bucket
// holds, calls or not, so a bucket nobody wrote shows as a gap.
func (e SpanEdge) IsMarker() bool { return e.Target == "" }

// SpanEdgeMarker returns the marker row of one bucket: no calls and an empty
// histogram.
func SpanEdgeMarker(step int64, bucket time.Time) SpanEdge {
	return SpanEdge{StepSeconds: step, Bucket: bucket, Hist: spanhist.Hist{}.Encode()}
}

const spanEdgeColumns = `step_seconds, bucket, project_id, source, target, target_type,
	count, error_count, weighted_count, weighted_spans, sum_duration_us, min_us, max_us, hist`

const spanEdgeKey = `step_seconds, bucket, project_id, source, target, target_type`

// spanEdgeAddSQL folds a flush into its row, as spanRollupAddSQL does.
const spanEdgeAddSQL = `INSERT INTO span_edges (` + spanEdgeColumns + `)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(` + spanEdgeKey + `)
	DO UPDATE SET
		count = count + excluded.count,
		error_count = error_count + excluded.error_count,
		weighted_count = weighted_count + excluded.weighted_count,
		weighted_spans = weighted_spans + excluded.weighted_spans,
		sum_duration_us = sum_duration_us + excluded.sum_duration_us,
		min_us = MIN(min_us, excluded.min_us),
		max_us = MAX(max_us, excluded.max_us),
		hist = ` + spanHistMergeFunc + `(hist, excluded.hist)`

// spanEdgeReplaceSQL writes a compacted row whole, as spanRollupReplaceSQL does.
const spanEdgeReplaceSQL = `INSERT OR REPLACE INTO span_edges (` + spanEdgeColumns + `)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func spanEdgeArgs(e SpanEdge) []any {
	return []any{
		e.StepSeconds, e.Bucket.Unix(), e.ProjectID, e.Source, e.Target, e.TargetType,
		e.Count, e.ErrorCount, e.WeightedCount, e.WeightedSpans, e.SumDurationUs, e.MinUs, e.MaxUs, e.Hist,
	}
}

func compareSpanEdgeKey(a, b SpanEdge) int {
	return cmp.Or(
		cmp.Compare(a.StepSeconds, b.StepSeconds),
		a.Bucket.Compare(b.Bucket),
		cmp.Compare(a.ProjectID, b.ProjectID),
		strings.Compare(a.Source, b.Source),
		strings.Compare(a.Target, b.Target),
		strings.Compare(a.TargetType, b.TargetType),
	)
}

// AddSpanEdges folds a flush of span edges into their rows, chunked like
// AddSpanRollups.
func (r *Repository) AddSpanEdges(rows []SpanEdge) error {
	return writeTierRows(r, context.Background(), "AddSpanEdges", spanEdgeAddSQL, rows, spanRollupChunk, compareSpanEdgeKey, spanEdgeArgs)
}

// ReplaceSpanEdges writes compacted rows whole, chunked like AddSpanRollups.
func (r *Repository) ReplaceSpanEdges(ctx context.Context, rows []SpanEdge) error {
	return writeTierRows(r, ctx, "ReplaceSpanEdges", spanEdgeReplaceSQL, rows, spanRollupChunk, compareSpanEdgeKey, spanEdgeArgs)
}

// SpanEdgeCoverage is SpanRollupCoverage for span_edges. The two tables share
// the compaction watermarks.
func (r *Repository) SpanEdgeCoverage(ctx context.Context) (SpanRollupCoverage, error) {
	return r.tierCoverage(ctx, "span_edges")
}

// OldestSpanEdgeBucket returns the earliest bucket of one span_edges tier.
func (r *Repository) OldestSpanEdgeBucket(ctx context.Context, step int64) (time.Time, bool, error) {
	return r.oldestTierBucket(ctx, "span_edges", step)
}

// CountSpanEdgeBuckets counts the buckets of one tier in [from, to) that hold
// at least one row, markers included.
func (r *Repository) CountSpanEdgeBuckets(ctx context.Context, step int64, from, to time.Time) (int, error) {
	return r.countTierBuckets(ctx, "span_edges", step, from, to)
}

// SpanEdgeScan selects span edge rows. Empty fields apply no filter.
type SpanEdgeScan struct {
	Segments  []SpanRollupSegment
	ProjectID int64
	Source    string
	// Markers includes the bucket markers. Readers leave them out; the
	// compactor carries them into the coarse tier.
	Markers bool
}

func (q SpanEdgeScan) where(seg SpanRollupSegment) ([]string, []any) {
	b := &whereBuilder{}
	b.add("step_seconds = ?", seg.Step)
	b.add("bucket >= ?", seg.From.Unix())
	b.add("bucket < ?", seg.To.Unix())
	if q.ProjectID != 0 {
		b.add("project_id = ?", q.ProjectID)
	}
	if q.Source != "" {
		b.add("source = ?", q.Source)
	}
	if !q.Markers {
		b.add("target <> ''")
	}
	return b.where, b.args
}

// ScanSpanEdges calls fn for every row q selects, one segment after the
// other. It stops at the first error fn returns.
func (r *Repository) ScanSpanEdges(ctx context.Context, q SpanEdgeScan, fn func(SpanEdge) error) error {
	ctx, cancel := r.readContext(ctx)
	defer cancel()
	for _, seg := range q.Segments {
		where, args := q.where(seg)
		rows, err := r.db.QueryContext(ctx, `SELECT `+spanEdgeColumns+` FROM span_edges WHERE `+
			strings.Join(where, " AND "), args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e SpanEdge
			var bucket int64
			if err = rows.Scan(&e.StepSeconds, &bucket, &e.ProjectID, &e.Source, &e.Target, &e.TargetType,
				&e.Count, &e.ErrorCount, &e.WeightedCount, &e.WeightedSpans, &e.SumDurationUs, &e.MinUs, &e.MaxUs, &e.Hist); err != nil {
				break
			}
			e.Bucket = time.Unix(bucket, 0).UTC()
			if err = fn(e); err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// SpanEdgeWindow returns the rows of one tier with buckets in [from, to),
// markers included.
func (r *Repository) SpanEdgeWindow(ctx context.Context, step int64, from, to time.Time) ([]SpanEdge, error) {
	var out []SpanEdge
	err := r.ScanSpanEdges(ctx, SpanEdgeScan{
		Segments: []SpanRollupSegment{{Step: step, From: from, To: to}},
		Markers:  true,
	}, func(e SpanEdge) error {
		out = append(out, e)
		return nil
	})
	return out, err
}

// DeleteSpanEdgesOlderThanLimited removes at most max rows of one tier with
// buckets before cutoff, oldest first, reporting whether more remain.
func (r *Repository) DeleteSpanEdgesOlderThanLimited(ctx context.Context, step int64, cutoff time.Time, max int64) (int64, bool, error) {
	return r.deleteTierRowsOlderThan(ctx, "span_edges", spanEdgeKey, step, cutoff, max)
}
