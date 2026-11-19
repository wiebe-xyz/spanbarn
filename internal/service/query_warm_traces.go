package service

import (
	"context"
	"fmt"
	"time"
)

// traceListWarmWindow is the range the traces page opens on, and traceListWarmLimit
// its page size. The warm reads exactly that shape so the pages the viewer's
// first read needs are already in the page cache (#457).
const (
	traceListWarmWindow = time.Hour
	traceListWarmLimit  = 50
)

// WarmTraceLists reads the newest hour of the trace list the way the traces
// page asks for it: once over all projects and once per project in projectIDs.
// The results are discarded. The reads pull the shard pages under the newest
// hour into the page cache, which a reader that was quiet for a while has lost
// (the first read after such a spell took 1.5 s, the repeat 0.07 s).
//
// The reads run one at a time and the loop stops between two of them when ctx
// ends. It returns how many reads were issued and the first error met.
func (s *QueryService) WarmTraceLists(ctx context.Context, projectIDs []int64, now time.Time) (int, error) {
	var first error
	warmed := 0
	for _, id := range append([]int64{0}, projectIDs...) {
		if err := ctx.Err(); err != nil {
			return warmed, err
		}
		_, err := s.SearchTraces(ctx, TraceSearchFilter{
			ProjectID:       id,
			RootOnly:        true,
			SortErrorsFirst: true,
			From:            now.Add(-traceListWarmWindow),
			To:              now,
			Limit:           traceListWarmLimit,
		})
		warmed++
		if err != nil && first == nil {
			first = fmt.Errorf("trace list project %d: %w", id, err)
		}
	}
	return warmed, first
}
