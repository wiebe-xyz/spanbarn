package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

// buildTiny generates all three layouts at 1/1000 of the prod profile into a
// test temp dir. Shared by the workstream tests.
func buildTiny(t *testing.T) (string, options) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "spike")
	opts, err := parseOptions("gen", []string{"-dir", dir, "-scale", "0.001", "-days", "3"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cmdGen(context.Background(), opts, &out); err != nil {
		t.Fatalf("gen: %v\n%s", err, out.String())
	}
	return dir, opts
}

func TestLayoutsHoldTheSameRows(t *testing.T) {
	dir, _ := buildTiny(t)
	ctx := context.Background()
	want, err := rowCounts(ctx, dir, layoutSingle)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range inScope {
		if want[tbl] == 0 {
			t.Errorf("single: %s is empty", tbl)
		}
	}
	for _, l := range []string{layoutSplit, layoutShards} {
		got, err := rowCounts(ctx, dir, l)
		if err != nil {
			t.Fatal(err)
		}
		for _, tbl := range inScope {
			if got[tbl] != want[tbl] {
				t.Errorf("%s.%s = %d rows, single has %d", l, tbl, got[tbl], want[tbl])
			}
		}
	}
}

func TestShardsSplitByDay(t *testing.T) {
	dir, opts := buildTiny(t)
	files, err := dataFiles(layoutDir(dir, layoutShards))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != opts.days {
		t.Fatalf("got %d shard files, want %d", len(files), opts.days)
	}
}

func TestGenRefusesExistingDir(t *testing.T) {
	opts, err := parseOptions("gen", []string{"-dir", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdGen(context.Background(), opts, &bytes.Buffer{}); err == nil {
		t.Fatal("gen into an existing dir succeeded")
	}
}
