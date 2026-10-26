package spanrollup

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func planString(segs []repository.SpanRollupSegment) string {
	var parts []string
	for _, s := range segs {
		parts = append(parts, fmt.Sprintf("%d:%s-%s", s.Step, s.From.Format("02T15:04"), s.To.Format("02T15:04")))
	}
	return strings.Join(parts, " ")
}

func TestPlan(t *testing.T) {
	d := func(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, time.UTC) }
	full := repository.SpanRollupCoverage{
		Oldest:  map[int64]time.Time{300: d(1, 0, 0), 3600: d(1, 0, 0), 86400: d(1, 0, 0)},
		Through: map[int64]time.Time{300: d(26, 9, 0), 3600: d(26, 0, 0)},
	}
	only5m := repository.SpanRollupCoverage{Oldest: map[int64]time.Time{300: d(25, 0, 0)}}

	for _, tc := range []struct {
		name     string
		from, to time.Time
		align    int64
		cov      repository.SpanRollupCoverage
		want     string // "" means not covered
	}{
		{"edges are whole 5m buckets", d(26, 10, 2), d(26, 10, 13), 300, full,
			"300:26T10:00-26T10:15"},
		{"hours in the middle when the chart allows it", d(26, 6, 50), d(26, 10, 10), 3600, full,
			"300:26T06:50-26T07:00 3600:26T07:00-26T09:00 300:26T09:00-26T10:10"},
		{"a chart of 30-minute buckets reads only 5m", d(26, 6, 0), d(26, 7, 0), 1800, full,
			"300:26T06:00-26T07:00"},
		{"days, then hours, then 5m for a total", d(24, 0, 0), d(26, 9, 30), 0, full,
			"86400:24T00:00-26T00:00 3600:26T00:00-26T09:00 300:26T09:00-26T09:30"},
		{"older than the 5m tier and no coarse tier", d(24, 23, 0), d(25, 1, 0), 300, only5m, ""},
		{"inside the 5m tier", d(25, 0, 5), d(25, 1, 0), 300, only5m, "300:25T00:05-25T01:00"},
		{"chart buckets that are not 5m multiples", d(26, 6, 0), d(26, 7, 0), 60, full, ""},
		{"empty range", d(26, 6, 0), d(26, 6, 0), 300, full, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			segs, ok := Plan(tc.from, tc.to, tc.align, tc.cov)
			got := planString(segs)
			if tc.want == "" {
				if ok {
					t.Errorf("covered with %q, want not covered", got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Errorf("plan = %q (ok=%v), want %q", got, ok, tc.want)
			}
		})
	}
}
