package service

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanrollup"
)

// Source labels say which store answered a read. span_rollups counts every
// span the writer received; raw spans hold only what was stored.
const (
	SourceRollups = "rollups"
	SourceSpans   = "spans"
)

// SpanRollupReads controls reads from span_rollups.
type SpanRollupReads struct {
	// Enabled lets a read use span_rollups when they cover its whole range
	// (SPANBARN_SPAN_ROLLUP_READS=auto). Off reads raw spans, as before.
	Enabled bool
	// HTTPStatus says the writers split root rows by response code
	// (SPANBARN_SPAN_ROLLUP_HTTP_STATUS). Off, grouping by HTTP status reads
	// raw spans, because the rollups hold 0 for every span.
	HTTPStatus bool
}

// spanRollupCoverageTTL is how long one coverage read serves. The oldest
// bucket and the watermarks move by minutes, and a stale value only shifts
// which source answers near a tier edge.
const spanRollupCoverageTTL = time.Minute

// spanRollupFlushGrace is how long a new 5-minute bucket may hold no rows
// before a read counts it as a gap. The writer flushes rollups every 30s.
const spanRollupFlushGrace = 2 * time.Minute

// spanRollupCoverage caches the coverage of span_rollups, or of span_edges,
// per QueryService. It is read
// inside cached scans, so it does not go through readCached: a nested
// readCached would wait for a compute slot its caller already holds.
type spanRollupCoverage struct {
	mu   sync.Mutex
	cov  repository.SpanRollupCoverage
	read time.Time
}

// SetSpanRollupReads configures reads from span_rollups.
func (s *QueryService) SetSpanRollupReads(r SpanRollupReads) {
	s.rollupReads = r
}

// spanRollupPlan returns the tier segments that cover [from, to) at chart
// buckets of align seconds, or ok false when the rollups do not cover the
// whole range, a bucket inside it holds no rows, or reads from them are off.
func (s *QueryService) spanRollupPlan(ctx context.Context, from, to time.Time, align int64) ([]repository.SpanRollupSegment, bool) {
	return s.tierPlan(ctx, from, to, align, &s.rollupCoverage, s.repo.SpanRollupCoverage, s.repo)
}

// spanEdgePlan is spanRollupPlan for span_edges.
func (s *QueryService) spanEdgePlan(ctx context.Context, from, to time.Time, align int64) ([]repository.SpanRollupSegment, bool) {
	return s.tierPlan(ctx, from, to, align, &s.edgeCoverage, s.repo.SpanEdgeCoverage,
		spanrollup.BucketCounterFunc(s.repo.CountSpanEdgeBuckets))
}

// tierPlan plans a read of a table compacted like span_rollups, from its
// cached coverage, and checks the planned buckets for gaps.
func (s *QueryService) tierPlan(ctx context.Context, from, to time.Time, align int64, c *spanRollupCoverage,
	read func(context.Context) (repository.SpanRollupCoverage, error), counter spanrollup.BucketCounter) ([]repository.SpanRollupSegment, bool) {
	if !s.rollupReads.Enabled {
		return nil, false
	}
	cov, err := c.get(ctx, read)
	if err != nil {
		s.logger.Info("span rollup coverage read failed, reading raw spans", "error", err)
		return nil, false
	}
	segs, ok := spanrollup.Plan(from.UTC(), to.UTC(), align, cov)
	if !ok {
		return nil, false
	}
	complete, err := spanrollup.Complete(ctx, counter, segs, time.Now().Add(-spanRollupFlushGrace))
	if err != nil {
		s.logger.Info("span rollup gap check failed, reading raw spans", "error", err)
		return nil, false
	}
	return segs, complete
}

// get returns the cached coverage, reading it again once it is older than
// spanRollupCoverageTTL.
func (c *spanRollupCoverage) get(ctx context.Context, read func(context.Context) (repository.SpanRollupCoverage, error)) (repository.SpanRollupCoverage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.read.IsZero() && time.Since(c.read) < spanRollupCoverageTTL {
		return c.cov, nil
	}
	cov, err := read(ctx)
	if err != nil {
		return cov, err
	}
	c.cov, c.read = cov, time.Now()
	return cov, nil
}

// rollupStatus reports whether a status filter is one span_rollups holds.
// Rollups normalise status to ok, error or unset.
func rollupStatus(status string) bool {
	switch status {
	case "", "ok", "error", "unset":
		return true
	}
	return false
}

// heatmapEdge returns the heatmap bucket whose lower edge is us. A dashboard
// duration band comes from heatmap brushing, so its bounds are such edges.
func heatmapEdge(us int64) (int, bool) {
	guess := int(math.Round(2 * math.Log2(float64(us)+1)))
	for _, i := range []int{guess - 1, guess, guess + 1} {
		if i >= 0 && repository.HeatmapBucketLowerUs(i) == us {
			return i, true
		}
	}
	return 0, false
}

// rollupBand maps a duration band to quarter-octave bins [lo, hi); hi 0 is
// open. A heatmap bucket is two quarter-octave bins, and the upper bound is
// the lower edge of the first bucket past the band. ok is false when a bound
// is not on a heatmap bucket edge, which span_rollups cannot answer.
func rollupBand(minUs, maxUs int64) (lo, hi int, ok bool) {
	if minUs > 0 {
		i, ok := heatmapEdge(minUs)
		if !ok {
			return 0, 0, false
		}
		lo = 2 * i
	}
	if maxUs > 0 {
		i, ok := heatmapEdge(maxUs)
		if !ok {
			return 0, 0, false
		}
		hi = 2 * i
	}
	return lo, hi, true
}

// traceGroupRollupMinWindow is the narrowest range trace groups read from
// span_rollups. The rollups read whole 5-minute buckets at both edges, which
// a window of an hour or more absorbs.
const traceGroupRollupMinWindow = time.Hour

// rootSpanGroups reads the root span groups from span_rollups when they can
// answer sf, and from the stored root spans otherwise. A minimum duration
// reads raw spans: it is a free value, rarely a bin edge.
func (s *QueryService) rootSpanGroups(ctx context.Context, sf repository.SpanFilter) ([]repository.RootSpanGroup, string, error) {
	if sf.MinDuration == 0 && rollupStatus(sf.Status) && !sf.From.IsZero() && !sf.To.IsZero() &&
		sf.To.Sub(sf.From) >= traceGroupRollupMinWindow {
		if segs, ok := s.spanRollupPlan(ctx, sf.From, sf.To, 0); ok {
			groups, err := s.repo.QueryRootSpanGroupsFromRollups(ctx, repository.SpanRollupScan{
				Segments: segs, ProjectID: sf.ProjectID, Service: sf.Service, Status: sf.Status,
				ExcludeNames: sf.ExcludeOperations,
			})
			return groups, SourceRollups, err
		}
	}
	groups, err := s.repo.QueryRootSpanGroups(ctx, sf)
	return groups, SourceSpans, err
}
