package retention

import (
	"context"
	"errors"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// cancelledAttr marks a cycle or phase span that stopped because its caller
// did, as the outgoing pod's cycle does at every rollout.
var cancelledAttr = attribute.Bool("cancelled", true)

// runPhases runs the cycle's three phases, each under its own span. The
// database calls inside a phase are counted onto that span as db.calls
// attributes; otelsql creates no span per statement under it. Once the
// context is cancelled no further phase starts: the cycle ends after the
// statement in flight, and the next pod's cycle picks up the rest.
func (w *RetentionWorker) runPhases(ctx context.Context, cycle trace.Span, cfg Config, cut cycleCutoffs, st *cycleStats) error {
	phases := []struct {
		name string
		run  func(context.Context) error
	}{
		{"retention.drain_spans", func(ctx context.Context) error {
			return w.drainOldSpans(ctx, cycle, cfg, cut.interesting, st)
		}},
		{"retention.purge_span_derived", func(ctx context.Context) error {
			return w.purgeSpanDerived(ctx, cfg, cut, st)
		}},
		{"retention.purge_housekeeping", func(ctx context.Context) error {
			return w.purgeHousekeeping(ctx, cut, st)
		}},
	}
	for _, p := range phases {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := runPhase(ctx, p.name, p.run); err != nil {
			return err
		}
	}
	return nil
}

func runPhase(ctx context.Context, name string, run func(context.Context) error) error {
	ctx, span := tracer.Start(ctx, name)
	defer span.End()
	ctx, calls := engine.WithStatementCounts(ctx)
	err := run(ctx)
	span.SetAttributes(calls.Attributes()...)
	markOutcome(span, err)
	return err
}

// stopped reports whether err only says the caller cancelled the work. That
// is a pod shutting down mid-cycle, which is no failure, yet two rollouts on
// 2026-11-05 each left an error span on the outgoing pod's cycle. A deadline
// stays an error, as it does for the sqlite spans (#382).
func stopped(err error) bool {
	return errors.Is(err, context.Canceled)
}

// markOutcome records err on span: a cancel as the cancelled attribute with no
// error status, any other error as an error.
func markOutcome(span trace.Span, err error) {
	switch {
	case err == nil:
	case stopped(err):
		span.SetAttributes(cancelledAttr)
	default:
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}
