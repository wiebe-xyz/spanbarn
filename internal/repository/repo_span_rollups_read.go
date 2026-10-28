package repository

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// SpanRollupCoverage says which buckets each span rollup tier holds. A tier
// missing from Oldest is empty. Through[step] is how far the tier of that step
// was compacted into the next one, so the next tier holds buckets before it.
type SpanRollupCoverage struct {
	Oldest  map[int64]time.Time
	Through map[int64]time.Time
}

// spanRollupSteps are the tiers of span_rollups, finest first.
var spanRollupSteps = []int64{300, 3600, 86400}

// SpanRollupCoverage reads the oldest bucket of every tier from the front of
// the primary key, and the compaction watermarks.
func (r *Repository) SpanRollupCoverage(ctx context.Context) (SpanRollupCoverage, error) {
	ctx, cancel := r.readContext(ctx)
	defer cancel()
	cov := SpanRollupCoverage{Oldest: map[int64]time.Time{}, Through: map[int64]time.Time{}}
	for _, step := range spanRollupSteps {
		oldest, ok, err := r.OldestSpanRollupBucket(ctx, step)
		if err != nil {
			return cov, err
		}
		if ok {
			cov.Oldest[step] = oldest
		}
		through, ok, err := r.SpanRollupWatermark(ctx, step)
		if err != nil {
			return cov, err
		}
		if ok {
			cov.Through[step] = through
		}
	}
	return cov, nil
}

// CountSpanRollupBuckets counts the buckets of one tier in [from, to) that
// hold at least one row, over every project. Each step seeks the next bucket
// in the primary key, so the cost follows the number of buckets and not the
// rows inside them.
func (r *Repository) CountSpanRollupBuckets(ctx context.Context, step int64, from, to time.Time) (int, error) {
	ctx, cancel := r.readContext(ctx)
	defer cancel()
	var n int
	err := r.db.QueryRowContext(ctx, `WITH RECURSIVE b(x) AS (
		SELECT (SELECT MIN(bucket) FROM span_rollups WHERE step_seconds = ?1 AND bucket >= ?2 AND bucket < ?3)
		UNION ALL
		SELECT (SELECT MIN(bucket) FROM span_rollups WHERE step_seconds = ?1 AND bucket > x AND bucket < ?3)
		FROM b WHERE x IS NOT NULL)
		SELECT COUNT(x) FROM b`, step, from.Unix(), to.Unix()).Scan(&n)
	return n, err
}

// SpanRollupSegment is a run of buckets of one tier: the buckets that start
// in [From, To).
type SpanRollupSegment struct {
	Step     int64
	From, To time.Time
}

// SpanRollupScan selects span rollup rows. Empty fields apply no filter.
type SpanRollupScan struct {
	Segments     []SpanRollupSegment
	ProjectID    int64
	Service      string
	Name         string
	Kind         string
	Status       string
	RootOnly     bool
	ExcludeNames []string
}

// where builds the conditions shared by every segment. The segment's own
// step and bucket range lead the WHERE clause, so each query reads one slice
// of the primary key.
func (q SpanRollupScan) where(seg SpanRollupSegment) ([]string, []any) {
	b := &whereBuilder{}
	b.add("step_seconds = ?", seg.Step)
	b.add("bucket >= ?", seg.From.Unix())
	b.add("bucket < ?", seg.To.Unix())
	if q.ProjectID != 0 {
		b.add("project_id = ?", q.ProjectID)
	}
	if q.Service != "" {
		b.add("service = ?", q.Service)
	}
	if q.Name != "" {
		b.add("name = ?", q.Name)
	}
	if q.Kind != "" {
		b.add("kind = ?", q.Kind)
	}
	if q.Status != "" {
		b.add("status = ?", q.Status)
	}
	if q.RootOnly {
		b.add("is_root = 1")
	}
	b.addNotIn("name", q.ExcludeNames)
	return b.where, b.args
}

// ScanSpanRollups calls fn for every row q selects, one segment after the
// other. It stops at the first error fn returns.
func (r *Repository) ScanSpanRollups(ctx context.Context, q SpanRollupScan, fn func(SpanRollup) error) error {
	ctx, cancel := r.readContext(ctx)
	defer cancel()
	for _, seg := range q.Segments {
		where, args := q.where(seg)
		rows, err := r.db.QueryContext(ctx, `SELECT `+spanRollupColumns+` FROM span_rollups WHERE `+
			strings.Join(where, " AND "), args...)
		if err != nil {
			return err
		}
		err = scanSpanRollupRows(rows, fn)
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func scanSpanRollupRows(rows *sql.Rows, fn func(SpanRollup) error) error {
	for rows.Next() {
		var s SpanRollup
		var bucket int64
		if err := rows.Scan(&s.StepSeconds, &bucket, &s.ProjectID, &s.Service, &s.Name, &s.Kind, &s.IsRoot, &s.Status, &s.HTTPStatus,
			&s.Count, &s.WeightedCount, &s.WeightedSpans, &s.SumDurationUs, &s.MinUs, &s.MaxUs, &s.Hist); err != nil {
			return err
		}
		s.Bucket = time.Unix(bucket, 0).UTC()
		if err := fn(s); err != nil {
			return err
		}
	}
	return rows.Err()
}

// QueryRootSpanGroupsFromRollups is QueryRootSpanGroups over the root rows of
// span_rollups: every root span the writer received, with its sample weights,
// grouped by operation and service. A histogram that does not decode is
// left out of the percentiles; its counts still add.
func (r *Repository) QueryRootSpanGroupsFromRollups(ctx context.Context, q SpanRollupScan) ([]RootSpanGroup, error) {
	q.RootOnly = true
	type groupKey struct{ operation, service string }
	byOp := map[groupKey]*RootSpanGroup{}
	var order []groupKey
	err := r.ScanSpanRollups(ctx, q, func(s SpanRollup) error {
		k := groupKey{s.Name, s.Service}
		g := byOp[k]
		if g == nil {
			g = &RootSpanGroup{Operation: s.Name, Service: s.Service}
			byOp[k] = g
			order = append(order, k)
		}
		g.Count += s.Count
		if s.Status == "error" {
			g.ErrorCount += s.Count
		}
		g.Weights.Count += s.WeightedCount
		g.Weights.Spans += s.WeightedSpans
		if h, err := spanhist.Decode(s.Hist); err == nil {
			g.Hist.Merge(h)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]RootSpanGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *byOp[k])
	}
	return out, nil
}
