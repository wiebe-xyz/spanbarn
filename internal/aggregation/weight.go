package aggregation

import "github.com/wiebe-xyz/spanbarn/internal/repository"

// spanWeights sums the sample weights of the spans folded into one aggregate.
// A span without a known weight (0) adds to neither field, so spans counts the
// weighted spans and a reader can tell a fully weighted bucket from a mixed one
// (see repository.Aggregate.WeightedEstimate).
type spanWeights struct {
	count int64
	spans int64
}

func (w *spanWeights) add(s repository.Span) {
	if s.SampleWeight <= 0 {
		return
	}
	w.count += int64(s.SampleWeight)
	w.spans++
}
