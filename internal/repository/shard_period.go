package repository

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// maxShardFiles is the most shard files one family may need at once: the
// files its retention window covers plus the current one. A family's read pool
// attaches all of them, and the modernc build refuses the 11th ATTACH.
const maxShardFiles = 9

const day = 24 * time.Hour

// mondayEpoch is the first Monday after the Unix epoch, the origin of
// multi-week periods.
var mondayEpoch = time.Date(1970, 1, 5, 0, 0, 0, 0, time.UTC)

// shardSpec says how one family's rows are cut into files: periods of n days,
// or of n ISO weeks, aligned in UTC.
type shardSpec struct {
	family Family
	weekly bool
	n      int
}

// newShardSpec picks the shortest period, in whole units of a day or an ISO
// week, at which retention plus the current period fits in maxShardFiles
// files: n = ceil(units / (maxShardFiles - 1)).
func newShardSpec(f Family, weekly bool, retention time.Duration) shardSpec {
	unit := day
	if weekly {
		unit = 7 * day
	}
	units := int((retention + unit - 1) / unit)
	if units < 1 {
		units = 1
	}
	return shardSpec{family: f, weekly: weekly, n: (units + maxShardFiles - 2) / (maxShardFiles - 1)}
}

// unit is the length of one base unit of s.
func (s shardSpec) unit() time.Duration {
	if s.weekly {
		return 7 * day
	}
	return day
}

// length is the length of one period of s.
func (s shardSpec) length() time.Duration { return time.Duration(s.n) * s.unit() }

// start returns the start of the period that holds t.
func (s shardSpec) start(t time.Time) time.Time {
	origin := time.Unix(0, 0).UTC()
	if s.weekly {
		origin = mondayEpoch
	}
	periods := int64(t.UTC().Sub(origin) / s.length())
	return origin.Add(time.Duration(periods) * s.length())
}

// file names the shard file of the period starting at start: the family and
// the start date, or the ISO week of the start for weekly families.
func (s shardSpec) file(start time.Time) string {
	if s.weekly {
		y, w := start.ISOWeek()
		return fmt.Sprintf("%s-%dw%02d.db", s.family, y, w)
	}
	return fmt.Sprintf("%s-%s.db", s.family, start.Format("20060102"))
}

// parseFile reverses file: it returns the period start named by name, and
// false if name is not one of s's shard files.
func (s shardSpec) parseFile(name string) (time.Time, bool) {
	stem, ok := strings.CutSuffix(name, ".db")
	if !ok {
		return time.Time{}, false
	}
	stem, ok = strings.CutPrefix(stem, s.family.String()+"-")
	if !ok {
		return time.Time{}, false
	}
	if !s.weekly {
		t, err := time.Parse("20060102", stem)
		return t, err == nil
	}
	ys, ws, ok := strings.Cut(stem, "w")
	y, yerr := strconv.Atoi(ys)
	w, werr := strconv.Atoi(ws)
	if !ok || yerr != nil || werr != nil || w < 1 || w > 53 {
		return time.Time{}, false
	}
	return isoWeekStart(y, w), true
}

// isoWeekStart returns the Monday that starts ISO week w of year y. 4 January
// is always in week 1.
func isoWeekStart(y, w int) time.Time {
	jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, time.UTC)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
	return monday.AddDate(0, 0, 7*(w-1))
}
