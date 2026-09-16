package rollup

import (
	"testing"
	"time"
)

func TestTruncateBucket(t *testing.T) {
	// A Wednesday, mid-afternoon.
	ts := time.Date(2026, 9, 16, 14, 37, 12, 0, time.UTC)

	tests := []struct {
		name string
		step int64
		want time.Time
	}{
		{"5m", Step5m, time.Date(2026, 9, 16, 14, 35, 0, 0, time.UTC)},
		{"hour", StepHour, time.Date(2026, 9, 16, 14, 0, 0, 0, time.UTC)},
		{"day", StepDay, time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)},
		{"week starts monday", StepWeek, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)},
		{"month", StepMonth, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TruncateBucket(ts, tc.step); !got.Equal(tc.want) {
				t.Errorf("TruncateBucket = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestTruncateBucketSundayBelongsToPreviousWeek guards the Sunday==0 to
// Monday==0 conversion, the easiest thing to get wrong here.
func TestTruncateBucketSundayBelongsToPreviousWeek(t *testing.T) {
	sunday := time.Date(2026, 9, 20, 23, 59, 0, 0, time.UTC)
	want := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	if got := TruncateBucket(sunday, StepWeek); !got.Equal(want) {
		t.Errorf("week of Sunday %s = %s, want %s", sunday, got, want)
	}
}

// TestNextAndPrevBucketAreCalendarAware: months are not a fixed number of
// seconds, so stepping must not drift.
func TestNextAndPrevBucketAreCalendarAware(t *testing.T) {
	jan := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := NextBucket(jan, StepMonth)
	if want := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC); !feb.Equal(want) {
		t.Fatalf("next month = %s, want %s", feb, want)
	}
	if back := PrevBucket(feb, StepMonth); !back.Equal(jan) {
		t.Errorf("previous month = %s, want %s", back, jan)
	}

	// December rolls the year.
	dec := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if got, want := NextBucket(dec, StepMonth), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("next month after December = %s, want %s", got, want)
	}
}

func TestBucketClosed(t *testing.T) {
	bucket := time.Date(2026, 9, 16, 14, 0, 0, 0, time.UTC)

	if BucketClosed(bucket, StepHour, bucket.Add(59*time.Minute)) {
		t.Error("a bucket still taking writes must not be compacted")
	}
	if !BucketClosed(bucket, StepHour, bucket.Add(time.Hour)) {
		t.Error("bucket ending exactly now is closed")
	}
}

func TestLadderChainsTiers(t *testing.T) {
	ladder := Ladder()
	if ladder[0].Source != Step5m {
		t.Errorf("ladder starts from the 5m tier, got %d", ladder[0].Source)
	}
	for i := 1; i < len(ladder); i++ {
		if ladder[i].Source != ladder[i-1].Step {
			t.Errorf("tier %s reads step %d, but the tier before it writes %d — each rung must build on the previous one, or a month would be merged from 5-minute rows",
				ladder[i].Label, ladder[i].Source, ladder[i-1].Step)
		}
	}
}
