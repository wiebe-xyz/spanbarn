package service

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const defaultSeriesGroups = 5

// seriesBucketSizes are the bucket lengths picked automatically, in seconds.
var seriesBucketSizes = []int64{60, 300, 900, 1800, 3600, 3 * 3600, 6 * 3600, 12 * 3600, 86400}

// pickBucket returns the smallest standard bucket that keeps the range under
// about 48 buckets.
func pickBucket(rangeSeconds int64) int64 {
	for _, b := range seriesBucketSizes {
		if rangeSeconds/b <= targetSeriesBuckets {
			return b
		}
	}
	return seriesBucketSizes[len(seriesBucketSizes)-1]
}

func groupKey(group []string) string { return strings.Join(group, "\x00") }

// zeroWhenEmpty reports whether a bucket with no spans reads as 0 for the
// calculation. Rates, averages, maxima and percentiles have no value there.
func zeroWhenEmpty(fn string) bool {
	return fn == repository.CalcCount || fn == repository.CalcSumDuration || fn == repository.CalcCountDistinct
}

// bucketGrid lists the bucket starts that cover from to to.
func (r AnalyzeRequest) bucketGrid(size int64) []int64 {
	first := r.From.Unix() / size * size
	var out []int64
	for b := first; b <= r.To.Unix(); b += size {
		out = append(out, b)
	}
	return out
}

// AnalyzeSeries runs one calculation over time for the largest groups, with
// the rest folded into an other line. It reads the table result first to pick
// the groups, then reuses its sample ratio so both views agree.
func (s *QueryService) AnalyzeSeries(ctx context.Context, req AnalyzeRequest) (*AnalyzeSeriesResponse, error) {
	ctx, span := tracer.Start(ctx, "query.analyze_series")
	span.SetAttributes(attribute.Int64("project_id", req.ProjectID), attribute.Int("group_by", len(req.GroupBy)))
	defer span.End()

	if len(req.Calcs) > 1 {
		return nil, analyzeInvalid("a time series takes one calculation")
	}
	req.OrderBy = ""
	size := req.BucketSeconds
	if size <= 0 {
		size = pickBucket(int64(req.To.Sub(req.From).Seconds()))
	}
	req.Limit = clampDefault(req.Limit, defaultSeriesGroups, maxSeriesGroups)
	q, err := req.query()
	if err != nil {
		return nil, err
	}
	grid := req.bucketGrid(size)
	if len(grid) > maxSeriesBuckets {
		return nil, analyzeInvalid("bucket of %ds makes %d buckets, at most %d", size, len(grid), maxSeriesBuckets)
	}

	table, err := s.repo.Analyze(ctx, q)
	if err != nil {
		return nil, err
	}
	q.BucketSeconds = size
	q.Sample = table.SampleEvery
	q.Only = make([][]string, len(table.Rows))
	for i, row := range table.Rows {
		q.Only[i] = row.Group
	}
	res, err := s.repo.Analyze(ctx, q)
	if err != nil {
		return nil, err
	}
	return buildSeries(req, q, size, grid, table, res), nil
}

func buildSeries(req AnalyzeRequest, q repository.AnalyzeQuery, size int64, grid []int64,
	table, res *repository.AnalyzeResult) *AnalyzeSeriesResponse {
	index := make(map[int64]int, len(grid))
	for i, b := range grid {
		index[b] = i
	}
	calc := q.Calcs[0]
	lines := map[string]*AnalyzeSeriesLine{}
	var order []string
	add := func(group []string, other bool) *AnalyzeSeriesLine {
		key := groupKey(group)
		if other {
			key = "\x01other"
		}
		if l, ok := lines[key]; ok {
			return l
		}
		l := &AnalyzeSeriesLine{Group: group, Other: other, Values: make([]*float64, len(grid))}
		if zeroWhenEmpty(calc.Fn) {
			zero := 0.0
			for i := range l.Values {
				l.Values[i] = &zero
			}
		}
		lines[key] = l
		order = append(order, key)
		return l
	}
	for _, row := range table.Rows {
		add(row.Group, false)
	}
	for _, row := range res.Rows {
		i, ok := index[row.Bucket]
		if !ok {
			continue
		}
		vals, _ := scaleRow(row, q.Calcs, res.SampleEvery)
		v := vals[0]
		add(row.Group, row.Other).Values[i] = &v
	}
	out := &AnalyzeSeriesResponse{
		GroupBy: req.GroupBy, Calc: req.Calcs0(), BucketSeconds: size, Buckets: grid,
		Series:  make([]AnalyzeSeriesLine, 0, len(order)),
		Scanned: res.Scanned, SampleEvery: res.SampleEvery, Truncated: res.Truncated, MaxSpans: q.MaxSpans,
	}
	if out.GroupBy == nil {
		out.GroupBy = []string{}
	}
	for _, k := range order {
		if !lines[k].Other {
			out.Series = append(out.Series, *lines[k])
		}
	}
	if l, ok := lines["\x01other"]; ok {
		out.Series = append(out.Series, *l)
	}
	return out
}

// Calcs0 is the one calculation of a series request, count by default.
func (r AnalyzeRequest) Calcs0() string {
	if len(r.Calcs) == 0 {
		return repository.CalcCount
	}
	return r.Calcs[0]
}
