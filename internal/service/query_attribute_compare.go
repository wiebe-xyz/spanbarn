package service

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const (
	defaultCompareMaxSpans = 10000
	hardCompareMaxSpans    = 50000
	defaultCompareKeys     = 20
	hardCompareKeys        = 100
	defaultCompareValues   = 5
	hardCompareValues      = 20
	// compareValueCap is how many distinct values per key a scan keeps.
	compareValueCap = 200
)

// AttributeCompareQuery scopes an attribute comparison. ProjectID, From, To and
// a non-empty Selection are required. A nil Baseline compares against every
// span of the project in the range, the selection included.
type AttributeCompareQuery struct {
	ProjectID int64
	From, To  time.Time
	Selection *filter.Expr
	Baseline  *filter.Expr
	// Sample keeps 1 span in N. 0 samples 1 in 20 above 24h and reads every span
	// below that; 1 forces an exact scan.
	Sample int
	// MaxSpans is the row cap on spans read per set, newest first.
	MaxSpans int
	// Limit caps the attributes returned and Top the values kept per attribute.
	Limit int
	Top   int
}

// AttributeSetSummary describes how many spans one side of the comparison read.
type AttributeSetSummary struct {
	Scanned   int64 `json:"scanned"`
	Truncated bool  `json:"truncated"`
}

// AttributeComparison is the ranked difference between a selection and a baseline.
type AttributeComparison struct {
	Selection AttributeSetSummary `json:"selection"`
	Baseline  AttributeSetSummary `json:"baseline"`
	// Sample is the 1-in-N ratio applied to both sets; 1 means every span was read.
	Sample   int `json:"sample"`
	MaxSpans int `json:"maxSpans"`
	// Attributes are ordered by total variation distance, largest first.
	Attributes []AttributeDifference `json:"attributes"`
}

func (q AttributeCompareQuery) validate() error {
	if q.Selection == nil || len(q.Selection.Filters) == 0 {
		return fmt.Errorf("%w: a selection filter is required", ErrInvalidAttributeRequest)
	}
	return AttributeQuery{ProjectID: q.ProjectID, From: q.From, To: q.To, Sample: q.Sample}.validate()
}

func (q AttributeCompareQuery) setWindow(expr *filter.Expr, sample int) repository.AttributeSetWindow {
	return repository.AttributeSetWindow{
		ProjectID: q.ProjectID,
		From:      q.From.UTC(),
		To:        q.To.UTC(),
		Expr:      expr,
		Sample:    sample,
		MaxSpans:  clampDefault(q.MaxSpans, defaultCompareMaxSpans, hardCompareMaxSpans),
		ValueCap:  compareValueCap,
	}
}

func compareSample(q AttributeCompareQuery) int {
	if q.Sample != 0 {
		return q.Sample
	}
	if q.To.Sub(q.From) > autoSampleAbove {
		return autoSampleRatio
	}
	return 1
}

// CompareAttributes ranks the attributes whose value distribution in the
// selection differs most from the baseline, by total variation distance. Both
// sets are read through the same sample ratio and row cap, so the cost is
// bounded by two scans of at most MaxSpans spans however large the database is.
func (s *QueryService) CompareAttributes(ctx context.Context, q AttributeCompareQuery) (*AttributeComparison, error) {
	if err := q.validate(); err != nil {
		return nil, err
	}
	if err := q.Selection.Validate(); err != nil {
		return nil, err
	}
	if err := q.Baseline.Validate(); err != nil {
		return nil, err
	}
	ctx, span := tracer.Start(ctx, "query.attributes.compare")
	defer span.End()

	sample := compareSample(q)
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID), attribute.Int("sample", sample))
	selW, baseW := q.setWindow(q.Selection, sample), q.setWindow(q.Baseline, sample)
	sel, err := s.repo.ScanAttributeSet(ctx, selW)
	if err != nil {
		return nil, err
	}
	base, err := s.repo.ScanAttributeSet(ctx, baseW)
	if err != nil {
		return nil, err
	}
	return &AttributeComparison{
		Selection:  AttributeSetSummary{Scanned: sel.Scanned, Truncated: sel.Scanned >= int64(selW.MaxSpans)},
		Baseline:   AttributeSetSummary{Scanned: base.Scanned, Truncated: base.Scanned >= int64(baseW.MaxSpans)},
		Sample:     sample,
		MaxSpans:   selW.MaxSpans,
		Attributes: rankAttributes(sel, base, clampDefault(q.Limit, defaultCompareKeys, hardCompareKeys), clampDefault(q.Top, defaultCompareValues, hardCompareValues)),
	}, nil
}
