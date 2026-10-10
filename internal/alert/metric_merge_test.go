package alert

import (
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Two series of a delta counter in one bucket add up to the bucket's increase,
// and the rate is that increase over the 5-minute bucket.
func TestMergeRollupsDeltaSumRate(t *testing.T) {
	base := time.Date(2026, 6, 16, 6, 0, 0, 0, time.UTC)
	row := func(fp string, at time.Time, sum float64) repository.MetricRollup {
		return repository.MetricRollup{Type: "sum", Temporality: "delta", AttrFingerprint: fp, Bucket: at, Count: 1, Sum: sum, Last: sum}
	}
	pts := mergeRollupsByBucket([]repository.MetricRollup{
		row("a", base, 1200), row("b", base, 1800),
		row("a", base.Add(5*time.Minute), 300),
	})
	got := metrics.Derive("sum", pts)
	if len(got) != 2 || got[0].Value != 10 || got[1].Value != 1 {
		t.Fatalf("got %+v, want rates 10 and 1", got)
	}
}
