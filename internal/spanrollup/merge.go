package spanrollup

import (
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// mergeTier folds rows into one row per key. first turns the first row of a
// key into that key's coarse row with its counts zeroed, add folds a row's
// counts into it, and hist points at a row's histogram. Histograms merge; one
// that does not decode is left out of the merge.
func mergeTier[R any, K comparable](rows []R, key func(R) K, first func(R) R, add func(*R, R), hist func(*R) *[]byte) []R {
	type acc struct {
		row  R
		hist spanhist.Hist
	}
	byKey := map[K]*acc{}
	var order []K
	for _, r := range rows {
		k := key(r)
		a := byKey[k]
		if a == nil {
			a = &acc{row: first(r)}
			byKey[k] = a
			order = append(order, k)
		}
		add(&a.row, r)
		if h, err := spanhist.Decode(*hist(&r)); err == nil {
			a.hist.Merge(h)
		}
	}
	out := make([]R, 0, len(order))
	for _, k := range order {
		a := byKey[k]
		*hist(&a.row) = a.hist.Encode()
		out = append(out, a.row)
	}
	return out
}

type rowKey struct {
	projectID           int64
	service, name, kind string
	root                bool
	status              string
	httpStatus          int64
}

// Merge folds span rollup rows into one row per key at the given step and
// bucket. Counts and sums add, min and max take the extreme and histograms
// merge.
func Merge(rows []repository.SpanRollup, step int64, bucket time.Time) []repository.SpanRollup {
	return mergeTier(rows,
		func(r repository.SpanRollup) rowKey {
			return rowKey{r.ProjectID, r.Service, r.Name, r.Kind, r.IsRoot, r.Status, r.HTTPStatus}
		},
		func(r repository.SpanRollup) repository.SpanRollup {
			r.StepSeconds, r.Bucket = step, bucket
			r.Count, r.WeightedCount, r.WeightedSpans, r.SumDurationUs = 0, 0, 0, 0
			return r
		},
		func(a *repository.SpanRollup, r repository.SpanRollup) {
			a.Count += r.Count
			a.WeightedCount += r.WeightedCount
			a.WeightedSpans += r.WeightedSpans
			a.SumDurationUs += r.SumDurationUs
			a.MinUs = min(a.MinUs, r.MinUs)
			a.MaxUs = max(a.MaxUs, r.MaxUs)
		},
		func(r *repository.SpanRollup) *[]byte { return &r.Hist })
}

type edgeKey struct {
	projectID                  int64
	source, target, targetType string
}

// MergeEdges folds span edge rows into one row per key at the given step and
// bucket, as Merge does. Bucket markers merge into one marker.
func MergeEdges(rows []repository.SpanEdge, step int64, bucket time.Time) []repository.SpanEdge {
	return mergeTier(rows,
		func(e repository.SpanEdge) edgeKey { return edgeKey{e.ProjectID, e.Source, e.Target, e.TargetType} },
		func(e repository.SpanEdge) repository.SpanEdge {
			e.StepSeconds, e.Bucket = step, bucket
			e.Count, e.ErrorCount, e.WeightedCount, e.WeightedSpans, e.SumDurationUs = 0, 0, 0, 0, 0
			return e
		},
		func(a *repository.SpanEdge, e repository.SpanEdge) {
			a.Count += e.Count
			a.ErrorCount += e.ErrorCount
			a.WeightedCount += e.WeightedCount
			a.WeightedSpans += e.WeightedSpans
			a.SumDurationUs += e.SumDurationUs
			a.MinUs = min(a.MinUs, e.MinUs)
			a.MaxUs = max(a.MaxUs, e.MaxUs)
		},
		func(e *repository.SpanEdge) *[]byte { return &e.Hist })
}
