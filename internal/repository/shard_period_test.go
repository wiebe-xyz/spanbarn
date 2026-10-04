package repository

import (
	"testing"
	"time"
)

func TestShardPeriodWidensPastNineFiles(t *testing.T) {
	cases := []struct {
		family    Family
		weekly    bool
		retention time.Duration
		n         int
	}{
		{FamilyLogs, false, 24 * time.Hour, 1},
		{FamilyLogs, false, 4 * time.Hour, 1},
		{FamilyMetrics, false, 7 * day, 1},
		{FamilyMetrics, false, 8 * day, 1},  // 8 days + today = 9 files
		{FamilyMetrics, false, 9 * day, 2},  // would be 10 files
		{FamilyMetrics, false, 14 * day, 2}, // 7 two-day files + the current one
		{FamilyMetrics, false, 30 * day, 4},
		{FamilyPrompts, true, 30 * day, 1},
		{FamilyPrompts, true, 70 * day, 2},
	}
	for _, c := range cases {
		s := newShardSpec(c.family, c.weekly, c.retention)
		if s.n != c.n {
			t.Errorf("%s %v: n = %d, want %d", c.family, c.retention, s.n, c.n)
		}
		if files := int((c.retention+s.length()-1)/s.length()) + 1; files > maxShardFiles {
			t.Errorf("%s %v: %d files", c.family, c.retention, files)
		}
	}
}

func TestShardPeriodStartAndFile(t *testing.T) {
	daily := newShardSpec(FamilyLogs, false, 24*time.Hour)
	weekly := newShardSpec(FamilyPrompts, true, 30*day)
	twoDay := newShardSpec(FamilyMetrics, false, 14*day)
	cases := []struct {
		spec shardSpec
		at   string
		file string
	}{
		{daily, "2026-10-04T23:59:59Z", "logs-20261004.db"},
		{daily, "2026-10-05T00:00:00Z", "logs-20261005.db"},
		{daily, "2026-10-05T01:00:00+02:00", "logs-20261004.db"}, // 23:00 UTC
		{weekly, "2026-10-04T23:59:59Z", "prompts-2026w40.db"},   // Sunday
		{weekly, "2026-10-05T00:00:00Z", "prompts-2026w41.db"},   // Monday
		{weekly, "2027-01-01T12:00:00Z", "prompts-2026w53.db"},   // ISO year 2026
		{twoDay, "2026-10-04T12:00:00Z", "metrics-20261004.db"},  // day 20730, even
		{twoDay, "2026-10-05T12:00:00Z", "metrics-20261004.db"},
		{twoDay, "2026-10-06T00:00:00Z", "metrics-20261006.db"},
	}
	for _, c := range cases {
		at, err := time.Parse(time.RFC3339, c.at)
		if err != nil {
			t.Fatal(err)
		}
		start := c.spec.start(at)
		if got := c.spec.file(start); got != c.file {
			t.Errorf("%s at %s: file %s, want %s", c.spec.family, c.at, got, c.file)
		}
		parsed, ok := c.spec.parseFile(c.file)
		if !ok || !parsed.Equal(start) {
			t.Errorf("parse %s = %v %v, want %v", c.file, parsed, ok, start)
		}
	}
}

func TestShardParseFileRejectsOtherNames(t *testing.T) {
	daily := newShardSpec(FamilyLogs, false, 24*time.Hour)
	weekly := newShardSpec(FamilyPrompts, true, 30*day)
	for _, name := range []string{"logs-20261004.db-wal", "metrics-20261004.db", "logs-2026.db", "logs-20261004"} {
		if _, ok := daily.parseFile(name); ok {
			t.Errorf("daily parsed %s", name)
		}
	}
	for _, name := range []string{"prompts-2026w54.db", "prompts-2026w00.db", "prompts-2026.db", "prompts-xw01.db"} {
		if _, ok := weekly.parseFile(name); ok {
			t.Errorf("weekly parsed %s", name)
		}
	}
}
