// Package tier is the calendar of metric rollup tiers: the step widths (5
// minutes, hour, day, week, month), the compaction ladder that orders them, and
// the bucket arithmetic (truncate, next, previous, closed) every consumer shares.
// Weeks start on Monday and months are calendar months, so bucket math lives
// here and nowhere else. The metric compactor, the span rollups, retention, the
// services and the API all read it.
package tier

import "time"

// Step widths, in seconds, of each rollup tier. StepMonth is nominal: monthly
// buckets are calendar months, so the stored step is a label rather than a
// duration to add.
const (
	Step5m    int64 = 300
	StepHour  int64 = 3600
	StepDay   int64 = 86400
	StepWeek  int64 = 604800
	StepMonth int64 = 2592000
)

// Tier is one rung of the compaction ladder: buckets of width Step built by
// merging the tier whose width is Source.
type Tier struct {
	Step   int64
	Source int64
	Label  string
}

// Ladder is the compaction order. Each tier is built from the one before it, so
// a month is merged from weeks rather than from 5-minute rows, keeping every
// pass small.
func Ladder() []Tier {
	return []Tier{
		{Step: StepHour, Source: Step5m, Label: "hourly"},
		{Step: StepDay, Source: StepHour, Label: "daily"},
		{Step: StepWeek, Source: StepDay, Label: "weekly"},
		{Step: StepMonth, Source: StepWeek, Label: "monthly"},
	}
}

// TruncateBucket returns the start of the bucket t falls in, in UTC.
//
// Weeks start Monday (ISO-8601) and months are calendar months, so neither is a
// fixed multiple of seconds from the epoch and both need their own arithmetic.
func TruncateBucket(t time.Time, step int64) time.Time {
	u := t.UTC()
	switch step {
	case StepMonth:
		return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	case StepWeek:
		day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
		// Go's Sunday==0 has to become Monday==0 for an ISO week.
		offset := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset)
	default:
		if step <= 0 {
			return u
		}
		d := time.Duration(step) * time.Second
		return u.Truncate(d)
	}
}

// NextBucket returns the start of the bucket after the one beginning at start.
// Together with TruncateBucket it is the only place bucket arithmetic happens,
// so calendar months stay correct everywhere.
func NextBucket(start time.Time, step int64) time.Time {
	switch step {
	case StepMonth:
		return start.AddDate(0, 1, 0)
	case StepWeek:
		return start.AddDate(0, 0, 7)
	default:
		if step <= 0 {
			return start
		}
		return start.Add(time.Duration(step) * time.Second)
	}
}

// PrevBucket returns the start of the bucket before the one beginning at start.
func PrevBucket(start time.Time, step int64) time.Time {
	switch step {
	case StepMonth:
		return start.AddDate(0, -1, 0)
	case StepWeek:
		return start.AddDate(0, 0, -7)
	default:
		if step <= 0 {
			return start
		}
		return start.Add(-time.Duration(step) * time.Second)
	}
}

// BucketClosed reports whether the bucket starting at start is complete as of
// now. Only closed buckets are compacted: a bucket still taking writes would be
// merged twice and counted twice.
func BucketClosed(start time.Time, step int64, now time.Time) bool {
	return !NextBucket(start, step).After(now.UTC())
}
