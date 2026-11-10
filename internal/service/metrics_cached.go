package service

import (
	"context"
	"fmt"
	"time"
)

// ReadMetricRange returns the cached result for a metric read over [from, to],
// computing it once on a miss. The range is snapped to its window's grain and
// compute receives the snapped range, so requests inside one grain share an
// entry. key names everything else the result depends on. Without a cache the
// read runs as asked.
func ReadMetricRange[T any](ctx context.Context, s *MetricsService, key string, from, to time.Time, compute func(ctx context.Context, from, to time.Time) (T, error)) (T, error) {
	from, to, grain := snapRange(from, to)
	k := fmt.Sprintf("%s:%d:%d", key, from.Unix(), to.Unix())
	return readCached(ctx, s.cache, k, grain, func(ctx context.Context) (T, error) {
		return compute(ctx, from, to)
	})
}
