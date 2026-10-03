package service

import (
	"context"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const (
	defaultHeatmapMaxSpans        = 20000
	hardHeatmapMaxSpans           = 100000
	defaultHeatmapTimeBuckets     = 60
	hardHeatmapTimeBuckets        = 200
	defaultHeatmapDurationBuckets = 30
	hardHeatmapDurationBuckets    = 100
)

func (q HeatmapQuery) validate() error {
	return AttributeQuery{ProjectID: q.ProjectID, From: q.From, To: q.To}.validate()
}

// Heatmap counts the spans of a filtered selection per time bucket and
// log-scale duration bucket. The scan reads at most MaxSpans spans, newest
// first, and the result says when that cap cut it short.
func (s *QueryService) Heatmap(ctx context.Context, q HeatmapQuery) (*HeatmapResult, error) {
	if err := q.validate(); err != nil {
		return nil, err
	}
	if err := q.Filter.Validate(); err != nil {
		return nil, err
	}
	ctx, span := tracer.Start(ctx, "query.heatmap")
	defer span.End()

	w := repository.HeatmapWindow{
		ProjectID:       q.ProjectID,
		From:            q.From.UTC(),
		To:              q.To.UTC(),
		Expr:            q.Filter,
		MaxSpans:        clampDefault(q.MaxSpans, defaultHeatmapMaxSpans, hardHeatmapMaxSpans),
		TimeBuckets:     clampDefault(q.TimeBuckets, defaultHeatmapTimeBuckets, hardHeatmapTimeBuckets),
		DurationBuckets: clampDefault(q.DurationBuckets, defaultHeatmapDurationBuckets, hardHeatmapDurationBuckets),
	}
	span.SetAttributes(attribute.Int64("project_id", q.ProjectID), attribute.Int("max_spans", w.MaxSpans))
	scan, err := s.repo.ScanHeatmap(ctx, w)
	if err != nil {
		return nil, err
	}
	cells := make([]HeatmapCell, len(scan.Cells))
	for i, c := range scan.Cells {
		cells[i] = HeatmapCell{Time: c.Time, Duration: c.Duration, Count: c.Count}
	}
	edges := scan.DurationEdgesUs
	if edges == nil {
		edges = []int64{}
	}
	return &HeatmapResult{
		From:            w.From,
		To:              w.To,
		TimeBuckets:     w.TimeBuckets,
		DurationBuckets: w.DurationBuckets,
		BucketMicros:    scan.BucketMicros,
		DurationEdgesUs: edges,
		Cells:           cells,
		Scanned:         scan.Scanned,
		Capped:          scan.Scanned >= int64(w.MaxSpans),
		MaxSpans:        w.MaxSpans,
	}, nil
}
