package aggregation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

var tracer = otel.Tracer("spanbarn/aggregation")

// AggregateWriter is the subset of repository methods needed to persist aggregates.
type AggregateWriter interface {
	UpsertAggregate(agg repository.Aggregate) error
}

// Aggregator groups raw spans into bucketed aggregates.
type Aggregator struct {
	repo     AggregateWriter
	interval time.Duration
	logger   *slog.Logger
}

// NewAggregator creates an Aggregator that buckets at the given interval.
func NewAggregator(repo AggregateWriter, interval time.Duration, logger *slog.Logger) *Aggregator {
	return &Aggregator{
		repo:     repo,
		interval: interval,
		logger:   logger,
	}
}

// groupKey identifies a unique aggregation group.
type groupKey struct {
	ProjectID int64
	Service   string
	Name      string
	Resource  string
	Kind      string
	Bucket    time.Time
}

// AggregateSpans groups the given spans by (project_id, service, name, resource, kind)
// and time bucket, computing count, error_count, percentiles, max, and sum for each group.
func (a *Aggregator) AggregateSpans(ctx context.Context, spans []repository.Span) ([]repository.Aggregate, error) {
	if len(spans) == 0 {
		return nil, nil
	}

	_, span := tracer.Start(ctx, "aggregation.aggregate_spans")
	span.SetAttributes(attribute.Int("input_span_count", len(spans)))
	defer span.End()

	groups := make(map[groupKey][]repository.Span)
	for _, s := range spans {
		bucket := TruncateToBucket(time.UnixMicro(s.StartTimeUs), a.interval)
		k := groupKey{
			ProjectID: s.ProjectID,
			Service:   s.Service,
			Name:      s.Name,
			Resource:  s.Resource,
			Kind:      s.Kind,
			Bucket:    bucket,
		}
		groups[k] = append(groups[k], s)
	}

	span.SetAttributes(attribute.Int("bucket_count", len(groups)))

	out := make([]repository.Aggregate, 0, len(groups))
	for k, bucket := range groups {
		durations := make([]int64, len(bucket))
		var errorCount int64
		var sumDuration int64
		var maxDuration int64

		for i, s := range bucket {
			durations[i] = s.DurationUs
			sumDuration += s.DurationUs
			if s.DurationUs > maxDuration {
				maxDuration = s.DurationUs
			}
			if s.Status == "error" {
				errorCount++
			}
		}

		out = append(out, repository.Aggregate{
			ProjectID:     k.ProjectID,
			Service:       k.Service,
			Operation:     k.Name,
			Resource:      k.Resource,
			Kind:          k.Kind,
			Bucket:        k.Bucket,
			Count:         int64(len(bucket)),
			ErrorCount:    errorCount,
			P50Us:         P50(durations),
			P95Us:         P95(durations),
			P99Us:         P99(durations),
			MaxUs:         maxDuration,
			SumDurationUs: sumDuration,
		})
	}

	return out, nil
}

// Persist writes each aggregate to the repository via UpsertAggregate.
func (a *Aggregator) Persist(ctx context.Context, aggregates []repository.Aggregate) error {
	_, span := tracer.Start(ctx, "aggregation.persist")
	span.SetAttributes(attribute.Int("aggregate_count", len(aggregates)))
	defer span.End()

	for _, agg := range aggregates {
		if err := a.repo.UpsertAggregate(agg); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return fmt.Errorf("upsert aggregate for %s/%s bucket %s: %w",
				agg.Service, agg.Operation, agg.Bucket.Format(time.RFC3339), err)
		}
	}
	a.logger.Info("persisted aggregates", "count", len(aggregates))
	return nil
}
