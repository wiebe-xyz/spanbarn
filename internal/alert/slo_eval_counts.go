package alert

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const (
	// sloBucket is the width of one stored count bucket.
	sloBucket = time.Minute

	// sloIngestLag is how long a bucket stays open after it ends. Spans reach
	// the spans table through the spool and the writer; the aggregation
	// accumulator flushes every 30s (cmd/spanbarn/mode_writer.go), so a span
	// ingested in the last seconds of a bucket can land up to one flush later.
	// Counting earlier would store a low total that is never corrected.
	sloIngestLag = 30 * time.Second

	// sloMaxCatchUp caps how many buckets one tick counts for one SLO. It bounds
	// the first run and the catch-up after a long outage to an hour of scans;
	// buckets older than that are skipped and stay a hole in the series.
	sloMaxCatchUp = 60
)

// sloFilters are the compiled good and total filters of one SLO.
type sloFilters struct {
	good, total *filter.Expr
}

func parseSLOFilters(s repository.SLO) (sloFilters, error) {
	good, err := filter.Parse(string(s.GoodFilter))
	if err != nil {
		return sloFilters{}, fmt.Errorf("good filter: %w", err)
	}
	total, err := filter.Parse(string(s.TotalFilter))
	if err != nil {
		return sloFilters{}, fmt.Errorf("total filter: %w", err)
	}
	return sloFilters{good: good, total: total}, nil
}

// sloCloseBefore returns the end of the newest bucket that is safe to count.
func sloCloseBefore(now time.Time) time.Time {
	return now.UTC().Add(-sloIngestLag).Truncate(sloBucket)
}

// sloCountStart returns the first bucket a tick must count. It resumes after
// the newest stored bucket, so a restart neither recounts nor leaves a hole,
// and never reaches back further than sloMaxCatchUp buckets.
func sloCountStart(latest, closeBefore time.Time) time.Time {
	floor := closeBefore.Add(-sloMaxCatchUp * sloBucket)
	if latest.IsZero() {
		return floor
	}
	if next := latest.UTC().Add(sloBucket); next.After(floor) {
		return next
	}
	return floor
}

// countSLO stores the counts of every closed, not yet counted bucket of s. On a
// failure it keeps the buckets counted so far, so the next tick resumes at the
// bucket that failed.
func (e *SLOEvaluator) countSLO(ctx context.Context, s repository.SLO, closeBefore time.Time) (err error) {
	ctx, span := alertTracer.Start(ctx, "alert.slo_count")
	span.SetAttributes(attribute.Int64("slo_id", s.ID))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	filters, err := parseSLOFilters(s)
	if err != nil {
		return err
	}
	latest, err := e.reads.LatestSLOBucket(s.ID)
	if err != nil {
		return fmt.Errorf("latest bucket: %w", err)
	}
	sampleRate := sampleRateFor(ctx, e.ratioLookup, s.ProjectID)

	var counts []repository.SLOCount
	var countErr error
	for b := sloCountStart(latest, closeBefore); b.Before(closeBefore); b = b.Add(sloBucket) {
		if ctx.Err() != nil {
			countErr = ctx.Err()
			break
		}
		c, err := e.countBucket(s, filters, b, sampleRate)
		if err != nil {
			countErr = fmt.Errorf("bucket %s: %w", b.Format(time.RFC3339), err)
			break
		}
		counts = append(counts, c)
	}
	span.SetAttributes(attribute.Int("buckets", len(counts)))
	if len(counts) > 0 {
		if err := e.writes.InsertSLOCounts(counts); err != nil {
			return fmt.Errorf("insert counts: %w", err)
		}
	}
	return countErr
}

// countBucket counts the good and total spans that were ingested in the bucket
// starting at b. Error spans are always kept by the sampler and only ok spans
// are sampled, so inflateCount scales the ok spans alone.
func (e *SLOEvaluator) countBucket(s repository.SLO, f sloFilters, b time.Time, sampleRate float64) (repository.SLOCount, error) {
	// The span filter bounds are inclusive, so the upper bound stops a
	// nanosecond short of the next bucket.
	base := repository.SpanFilter{ProjectID: s.ProjectID, From: b, To: b.Add(sloBucket - time.Nanosecond)}

	gf := base
	gf.Expr = f.good
	good, goodErr, err := e.reads.CountSpans(gf)
	if err != nil {
		return repository.SLOCount{}, fmt.Errorf("count good: %w", err)
	}
	tf := base
	tf.Expr = f.total
	total, totalErr, err := e.reads.CountSpans(tf)
	if err != nil {
		return repository.SLOCount{}, fmt.Errorf("count total: %w", err)
	}

	good = inflateCount(good, goodErr, sampleRate)
	total = inflateCount(total, totalErr, sampleRate)
	if good > total {
		// A good filter wider than the total filter, or a rounding difference
		// between the two scalings, must not report a negative bad count.
		good = total
	}
	return repository.SLOCount{SLOID: s.ID, BucketStart: b, Good: good, Total: total}, nil
}
