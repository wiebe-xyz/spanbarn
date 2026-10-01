package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ErrInvalidAttributeRequest marks an attribute discovery request the caller
// must fix (missing project, missing or oversized range, out of range knob).
// The API maps it to HTTP 400.
var ErrInvalidAttributeRequest = errors.New("invalid attribute request")

// MaxAttributeWindow is the widest range attribute discovery serves.
const MaxAttributeWindow = 7 * 24 * time.Hour

const (
	// autoSampleAbove is the window above which discovery samples by default,
	// per the storage decision in deploy/docs/attribute-storage-design.md.
	autoSampleAbove = 24 * time.Hour
	autoSampleRatio = 20
	maxSampleRatio  = 1000

	defaultAttributeMaxSpans = 20000
	hardAttributeMaxSpans    = 100000
	defaultAttributeKeys     = 50
	hardAttributeKeys        = 200
	defaultAttributeTop      = 5
	hardAttributeTop         = 20
	hardAttributeKeyTop      = 100
	attributeDistinctCap     = 1000
)

// AttributeQuery scopes attribute discovery. ProjectID, From and To are
// required. Zero values for the other fields pick the defaults.
type AttributeQuery struct {
	ProjectID int64
	From, To  time.Time
	SpanName  string
	Service   string
	// Key restricts the result to one key, for value autocomplete.
	Key string
	// Sample keeps 1 span in N. 0 samples 1 in 20 above 24h and reads every span
	// below that; 1 forces an exact scan.
	Sample int
	// MaxSpans is the row cap on spans read, newest first.
	MaxSpans int
	// Limit caps the keys returned and Top the values kept per key.
	Limit int
	Top   int
}

// AttributeValue is one observed value and the number of scanned spans with it.
type AttributeValue struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// AttributeKey describes one attribute key over the scanned spans.
type AttributeKey struct {
	Key string `json:"key"`
	// Spans is the number of scanned spans that populate the key and Coverage is
	// that number as a share of Scanned.
	Spans    int64   `json:"spans"`
	Coverage float64 `json:"coverage"`
	// Distinct is the number of distinct values, stopping at the distinct cap.
	Distinct       int64            `json:"distinct"`
	DistinctCapped bool             `json:"distinctCapped"`
	Top            []AttributeValue `json:"top"`
}

// AttributeDiscovery is the result of attribute discovery. Counts are over the
// scanned spans, not scaled to the window.
type AttributeDiscovery struct {
	// Scanned is how many spans were read, after sampling.
	Scanned int64 `json:"scanned"`
	// Sample is the 1-in-N ratio applied; 1 means every span was read.
	Sample int `json:"sample"`
	// Truncated is true when the scan stopped at MaxSpans before the window ran out.
	Truncated bool           `json:"truncated"`
	MaxSpans  int            `json:"maxSpans"`
	Keys      []AttributeKey `json:"keys"`
}

func clampDefault(v, def, hard int) int {
	if v <= 0 {
		return def
	}
	return min(v, hard)
}

func (q AttributeQuery) validate() error {
	switch {
	case q.ProjectID == 0:
		return fmt.Errorf("%w: project_id is required", ErrInvalidAttributeRequest)
	case q.From.IsZero() || q.To.IsZero():
		return fmt.Errorf("%w: from and to are required", ErrInvalidAttributeRequest)
	case !q.To.After(q.From):
		return fmt.Errorf("%w: to must be after from", ErrInvalidAttributeRequest)
	case q.To.Sub(q.From) > MaxAttributeWindow:
		return fmt.Errorf("%w: range is limited to %s", ErrInvalidAttributeRequest, MaxAttributeWindow)
	case q.Sample < 0 || q.Sample > maxSampleRatio:
		return fmt.Errorf("%w: sample must be between 1 and %d", ErrInvalidAttributeRequest, maxSampleRatio)
	}
	return nil
}

func (q AttributeQuery) window() (repository.AttributeWindow, error) {
	if err := q.validate(); err != nil {
		return repository.AttributeWindow{}, err
	}
	sample := q.Sample
	if sample == 0 {
		sample = 1
		if q.To.Sub(q.From) > autoSampleAbove {
			sample = autoSampleRatio
		}
	}
	topCap := hardAttributeTop
	if q.Key != "" {
		topCap = hardAttributeKeyTop
	}
	return repository.AttributeWindow{
		ProjectID:   q.ProjectID,
		From:        q.From.UTC(),
		To:          q.To.UTC(),
		SpanName:    q.SpanName,
		Service:     q.Service,
		Key:         q.Key,
		Sample:      sample,
		MaxSpans:    clampDefault(q.MaxSpans, defaultAttributeMaxSpans, hardAttributeMaxSpans),
		MaxKeys:     clampDefault(q.Limit, defaultAttributeKeys, hardAttributeKeys),
		TopValues:   clampDefault(q.Top, defaultAttributeTop, topCap),
		DistinctCap: attributeDistinctCap,
	}, nil
}

// DiscoverAttributes lists the attribute keys of the spans in the window with
// the share of spans that populate each, the number of distinct values and the
// top values. The range is required and capped at 7 days, the scan reads at most
// MaxSpans spans, and windows over 24h read a 1-in-20 sample unless the caller
// picks a ratio.
func (s *QueryService) DiscoverAttributes(ctx context.Context, q AttributeQuery) (*AttributeDiscovery, error) {
	w, err := q.window()
	if err != nil {
		return nil, err
	}
	ctx, span := tracer.Start(ctx, "query.attributes.discover")
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID), attribute.Int("sample", w.Sample))
	defer span.End()

	scan, err := s.repo.ScanAttributes(ctx, w)
	if err != nil {
		return nil, err
	}
	out := &AttributeDiscovery{
		Scanned:   scan.Scanned,
		Sample:    w.Sample,
		Truncated: scan.Scanned >= int64(w.MaxSpans),
		MaxSpans:  w.MaxSpans,
		Keys:      make([]AttributeKey, 0, len(scan.Keys)),
	}
	for _, k := range scan.Keys {
		ak := AttributeKey{
			Key:            k.Key,
			Spans:          k.Spans,
			Coverage:       float64(k.Spans) / float64(scan.Scanned),
			Distinct:       k.Distinct,
			DistinctCapped: k.DistinctCapped,
			Top:            make([]AttributeValue, 0, len(k.Top)),
		}
		for _, v := range k.Top {
			ak.Top = append(ak.Top, AttributeValue(v))
		}
		out.Keys = append(out.Keys, ak)
	}
	return out, nil
}
