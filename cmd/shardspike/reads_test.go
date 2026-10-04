package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestReadsPrintsTablePlansAndAttachCost(t *testing.T) {
	_, opts := buildTiny(t)
	opts.runs = 2
	var out bytes.Buffer
	if err := cmdReads(context.Background(), opts, &out); err != nil {
		t.Fatalf("reads: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{"## Reads (idle)", "### Query plans", "### Attach cost", "Attach limit", "L1 single", "L2 split", "L3 shards"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(got, "MISMATCH") {
		t.Errorf("layouts disagree:\n%s", got)
	}
	if strings.Contains(got, "failed:") || strings.Contains(got, "| error") {
		t.Errorf("a query failed:\n%s", got)
	}
	ids, err := pickIDs(context.Background(), opts.dir)
	if err != nil {
		t.Fatal(err)
	}
	cases, _ := readCases(opts.window(), ids)
	for _, c := range cases {
		if !strings.Contains(got, "| "+c.label+" |") {
			t.Errorf("table lacks row %q", c.label)
		}
	}
}

func TestReadsRangesTouchOneTwoAndAllShards(t *testing.T) {
	w := newWindow(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), 7)
	r := rangeCases(w)
	if len(r) != len(rangeNames) {
		t.Fatalf("%d ranges for %d names", len(r), len(rangeNames))
	}
	day := func(t time.Time) string { return t.Format("2006-01-02") }
	if day(r[0].from) != day(r[0].to) {
		t.Errorf("1 shard range spans days: %v", r[0])
	}
	if day(r[1].from) == day(r[1].to) {
		t.Errorf("2 shard range stays in one day: %v", r[1])
	}
	if !r[2].from.Equal(w.Start) || !r[2].to.Equal(w.End) {
		t.Errorf("full range = %v", r[2])
	}
}

func TestRowsMatchFlagsDifferentSizes(t *testing.T) {
	res := map[string][]cellResult{
		layoutSingle: {{size: 5}}, layoutSplit: {{size: 5}}, layoutShards: {{size: 4}},
	}
	if got := rowsMatch(res, 0); !strings.HasPrefix(got, "MISMATCH") || !strings.Contains(got, "L3=4") {
		t.Errorf("rowsMatch = %q", got)
	}
	res[layoutShards][0].size = 5
	if got := rowsMatch(res, 0); !strings.HasPrefix(got, "yes") {
		t.Errorf("rowsMatch = %q", got)
	}
}
