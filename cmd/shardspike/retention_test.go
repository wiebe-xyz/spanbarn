package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRetentionLeavesLayoutsUntouched(t *testing.T) {
	dir, opts := buildTiny(t)
	opts.duration = 2 * time.Second
	ctx := context.Background()
	before := map[string]map[string]int64{}
	for _, l := range []string{layoutSingle, layoutShards} {
		c, err := rowCounts(ctx, dir, l)
		if err != nil {
			t.Fatal(err)
		}
		before[l] = c
	}

	var out bytes.Buffer
	if err := cmdRetention(ctx, opts, &out); err != nil {
		t.Fatalf("retention: %v\n%s", err, out.String())
	}
	got := out.String()
	t.Log(got)
	for _, want := range []string{"## Retention: expire the oldest day", "L1 row DELETE", "L3 file delete", "| insert p99 |"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "did not finish") {
		t.Errorf("tiny delete did not finish within %s:\n%s", opts.duration, got)
	}

	for l, want := range before {
		after, err := rowCounts(ctx, dir, l)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(after, want) {
			t.Errorf("%s changed: before %v, after %v", l, want, after)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, retentionWorkDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("work dir still exists: %v", err)
	}
}

func TestRetentionDeletesTheOldestDay(t *testing.T) {
	dir, opts := buildTiny(t)
	opts.duration = 2 * time.Second
	ws := &retWork{dir: filepath.Join(dir, retentionWorkDir)}
	if err := os.Mkdir(ws.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ws.cleanup(); err != nil {
			t.Error(err)
		}
	})
	res, tables, err := expireSingle(context.Background(), opts, ws)
	if err != nil {
		t.Fatal(err)
	}
	if res.timedOut || res.rowsRemoved == 0 {
		t.Fatalf("timedOut=%v rowsRemoved=%d", res.timedOut, res.rowsRemoved)
	}
	for _, tb := range tables {
		if tb.table == "spans" && (tb.rows == 0 || tb.left != 0) {
			t.Errorf("spans: deleted %d, %d older rows left", tb.rows, tb.left)
		}
	}
	if len(res.inserts) == 0 {
		t.Error("no inserts recorded during the delete")
	}
}
