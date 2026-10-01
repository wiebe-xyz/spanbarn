package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// MaxAnalyzeWindow is the widest range a group-by query serves.
const MaxAnalyzeWindow = 30 * 24 * time.Hour

const (
	defaultAnalyzeGroups = 20
	hardAnalyzeGroups    = 100
	maxAnalyzeGroupBy    = 4
	maxAnalyzeCalcs      = 9
	defaultAnalyzeSpans  = 200_000
	hardAnalyzeSpans     = 500_000
	maxAnalyzeSample     = 1000
	maxSeriesGroups      = 10
	maxSeriesBuckets     = 500
	targetSeriesBuckets  = 48
)

// AnalyzeRequest is a group-by query. ProjectID, From, To and Calcs are
// required. Calcs use the wire names count, error_rate, sum_duration,
// avg_duration, max_duration, p50, p95, p99 and count_distinct:<attribute>.
type AnalyzeRequest struct {
	ProjectID int64
	From, To  time.Time
	Expr      *filter.Expr
	GroupBy   []string
	Calcs     []string
	// OrderBy names one of Calcs and defaults to the first. Asc sorts low to high.
	OrderBy string
	Asc     bool
	// Limit caps the groups before the other row.
	Limit int
	// Sample is 1 in N, 0 picks automatically and 1 reads every span. MaxSpans
	// caps the spans read.
	Sample   int
	MaxSpans int
	// BucketSeconds sets the series bucket. 0 picks about 48 buckets.
	BucketSeconds int64
}

// AnalyzeRow is one group of a table result. Values follow AnalyzeResponse.Calcs.
// Durations are microseconds and error_rate is a fraction.
type AnalyzeRow struct {
	Group  []string  `json:"group"`
	Other  bool      `json:"other,omitempty"`
	Count  int64     `json:"count"`
	Values []float64 `json:"values"`
	// Drill is the filter that selects the spans of this group, the request
	// filter included. Nil for the other row and when it cannot be expressed.
	Drill *filter.Expr `json:"drill,omitempty"`
}

// AnalyzeResponse is a table result. Counts and sums are scaled by SampleEvery
// when the scan was sampled, percentiles, maxima and distinct counts are not.
type AnalyzeResponse struct {
	GroupBy     []string     `json:"groupBy"`
	Calcs       []string     `json:"calcs"`
	Rows        []AnalyzeRow `json:"rows"`
	Other       *AnalyzeRow  `json:"other,omitempty"`
	Scanned     int64        `json:"scanned"`
	SampleEvery int          `json:"sampleEvery"`
	Truncated   bool         `json:"truncated"`
	MaxSpans    int          `json:"maxSpans"`
}

// AnalyzeSeriesLine is one group over time. Values align with the buckets of
// the response and are null where the group has no span in the bucket and the
// calculation has no zero.
type AnalyzeSeriesLine struct {
	Group  []string   `json:"group"`
	Other  bool       `json:"other,omitempty"`
	Values []*float64 `json:"values"`
}

// AnalyzeSeriesResponse is a time series result for one calculation.
type AnalyzeSeriesResponse struct {
	GroupBy       []string            `json:"groupBy"`
	Calc          string              `json:"calc"`
	BucketSeconds int64               `json:"bucketSeconds"`
	Buckets       []int64             `json:"buckets"`
	Series        []AnalyzeSeriesLine `json:"series"`
	Scanned       int64               `json:"scanned"`
	SampleEvery   int                 `json:"sampleEvery"`
	Truncated     bool                `json:"truncated"`
	MaxSpans      int                 `json:"maxSpans"`
}

func analyzeInvalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", filter.ErrInvalid, fmt.Sprintf(format, a...))
}

// parseCalc reads a wire calculation name.
func parseCalc(raw string) (repository.AnalyzeCalc, error) {
	fn, key, hasKey := strings.Cut(raw, ":")
	switch fn {
	case repository.CalcCount, repository.CalcErrorRate, repository.CalcSumDuration,
		repository.CalcAvgDuration, repository.CalcMaxDuration,
		repository.CalcP50, repository.CalcP95, repository.CalcP99:
		if hasKey {
			return repository.AnalyzeCalc{}, analyzeInvalid("%s takes no attribute", fn)
		}
		return repository.AnalyzeCalc{Fn: fn}, nil
	case repository.CalcCountDistinct:
		if key == "" {
			return repository.AnalyzeCalc{}, analyzeInvalid("count_distinct needs an attribute, as count_distinct:<attribute>")
		}
		if err := filter.ValidateKey(key); err != nil {
			return repository.AnalyzeCalc{}, err
		}
		return repository.AnalyzeCalc{Fn: fn, Key: key}, nil
	}
	return repository.AnalyzeCalc{}, analyzeInvalid("unknown calculation %q", raw)
}

func (r AnalyzeRequest) validate() error {
	switch {
	case r.ProjectID == 0:
		return analyzeInvalid("project_id is required")
	case r.From.IsZero() || r.To.IsZero():
		return analyzeInvalid("from and to are required")
	case !r.To.After(r.From):
		return analyzeInvalid("to must be after from")
	case r.To.Sub(r.From) > MaxAnalyzeWindow:
		return analyzeInvalid("range is limited to %s", MaxAnalyzeWindow)
	case r.Sample < 0 || r.Sample > maxAnalyzeSample:
		return analyzeInvalid("sample must be between 1 and %d", maxAnalyzeSample)
	case len(r.GroupBy) > maxAnalyzeGroupBy:
		return analyzeInvalid("group by at most %d keys", maxAnalyzeGroupBy)
	case len(r.Calcs) > maxAnalyzeCalcs:
		return analyzeInvalid("at most %d calculations", maxAnalyzeCalcs)
	}
	seen := map[string]bool{}
	for _, k := range r.GroupBy {
		if err := filter.ValidateKey(k); err != nil {
			return err
		}
		if seen[k] {
			return analyzeInvalid("group by %q twice", k)
		}
		seen[k] = true
	}
	return nil
}

// query turns a request into a repository query.
func (r AnalyzeRequest) query() (repository.AnalyzeQuery, error) {
	if err := r.validate(); err != nil {
		return repository.AnalyzeQuery{}, err
	}
	names := r.Calcs
	if len(names) == 0 {
		names = []string{repository.CalcCount}
	}
	q := repository.AnalyzeQuery{
		ProjectID: r.ProjectID, From: r.From, To: r.To, Expr: r.Expr,
		GroupBy:  r.GroupBy,
		Desc:     !r.Asc,
		Limit:    clampDefault(r.Limit, defaultAnalyzeGroups, hardAnalyzeGroups),
		Sample:   r.Sample,
		MaxSpans: clampDefault(r.MaxSpans, defaultAnalyzeSpans, hardAnalyzeSpans),
	}
	for i, n := range names {
		c, err := parseCalc(n)
		if err != nil {
			return q, err
		}
		q.Calcs = append(q.Calcs, c)
		if n == r.OrderBy {
			q.OrderBy = i
		}
	}
	if r.OrderBy != "" && !contains(names, r.OrderBy) {
		return q, analyzeInvalid("order_by %q is not one of the calculations", r.OrderBy)
	}
	return q, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// scales reports whether a calculation grows with the number of spans, so a
// sampled scan has to multiply it back up.
func scales(fn string) bool {
	return fn == repository.CalcCount || fn == repository.CalcSumDuration
}

func scaleRow(row repository.AnalyzeRow, calcs []repository.AnalyzeCalc, n int) ([]float64, int64) {
	vals := append([]float64{}, row.Values...)
	if n <= 1 {
		return vals, row.Count
	}
	for i, c := range calcs {
		if scales(c.Fn) {
			vals[i] *= float64(n)
		}
	}
	return vals, row.Count * int64(n)
}

func (r AnalyzeRequest) drill(group []string) *filter.Expr {
	conds := make([]filter.Node, len(group))
	for i, v := range group {
		conds[i] = filter.GroupCondition(r.GroupBy[i], v)
	}
	expr, ok := filter.Conjoin(r.Expr, conds)
	if !ok {
		return nil
	}
	return expr
}

func (r AnalyzeRequest) toRow(row repository.AnalyzeRow, q repository.AnalyzeQuery, every int) AnalyzeRow {
	vals, count := scaleRow(row, q.Calcs, every)
	out := AnalyzeRow{Group: row.Group, Other: row.Other, Count: count, Values: vals}
	if !row.Other && len(row.Group) > 0 {
		out.Drill = r.drill(row.Group)
	}
	return out
}

// Analyze runs a group-by query and returns a table: one row per group for the
// requested calculations, plus one row for the groups beyond the cap.
func (s *QueryService) Analyze(ctx context.Context, req AnalyzeRequest) (*AnalyzeResponse, error) {
	ctx, span := tracer.Start(ctx, "query.analyze")
	span.SetAttributes(attribute.Int64("project_id", req.ProjectID), attribute.Int("group_by", len(req.GroupBy)))
	defer span.End()

	q, err := req.query()
	if err != nil {
		return nil, err
	}
	res, err := s.repo.Analyze(ctx, q)
	if err != nil {
		return nil, err
	}
	out := &AnalyzeResponse{
		GroupBy: req.GroupBy, Rows: []AnalyzeRow{},
		Scanned: res.Scanned, SampleEvery: res.SampleEvery, Truncated: res.Truncated, MaxSpans: q.MaxSpans,
	}
	out.Calcs = req.Calcs
	if len(out.Calcs) == 0 {
		out.Calcs = []string{repository.CalcCount}
	}
	if out.GroupBy == nil {
		out.GroupBy = []string{}
	}
	for _, row := range res.Rows {
		out.Rows = append(out.Rows, req.toRow(row, q, res.SampleEvery))
	}
	if res.Other != nil {
		other := req.toRow(*res.Other, q, res.SampleEvery)
		out.Other = &other
	}
	return out, nil
}
