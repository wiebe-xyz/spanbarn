package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestContentionWritesToTheRightFiles(t *testing.T) {
	dir, opts := buildTiny(t)
	opts.duration = 2 * time.Second
	ctx := context.Background()
	split := layoutDir(dir, layoutSplit)
	before := map[string]map[string]int64{}
	for _, l := range []string{layoutSingle, layoutSplit} {
		before[l] = mustCounts(t, dir, l)
	}
	heavyBefore := fileCounts(t, filepath.Join(split, "heavy.db"), splitFiles["heavy.db"])

	var out bytes.Buffer
	if err := cmdContention(ctx, opts, &out); err != nil {
		t.Fatalf("contention: %v\n%s", err, out.String())
	}
	got := out.String()
	t.Log(got)
	for _, want := range []string{"## Span reads under write load (L1 vs L2)", "L1 single loaded", "L2 split loaded",
		"trace list p95", "trace detail p99", "analyze 24h p50", "span insert max", "WAL peak, heavy writer file"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	for _, l := range []string{layoutSingle, layoutSplit} {
		after := mustCounts(t, dir, l)
		if after["spans"] <= before[l]["spans"] {
			t.Errorf("%s: spans %d -> %d, want growth", l, before[l]["spans"], after["spans"])
		}
	}
	heavyAfter := fileCounts(t, filepath.Join(split, "heavy.db"), splitFiles["heavy.db"])
	for _, tbl := range splitFiles["heavy.db"] {
		if heavyAfter[tbl] <= heavyBefore[tbl] {
			t.Errorf("heavy.db %s: %d -> %d, want growth", tbl, heavyBefore[tbl], heavyAfter[tbl])
		}
	}
	assertNoTables(t, filepath.Join(split, "spans.db"), splitFiles["heavy.db"])
	assertNoTables(t, filepath.Join(split, "heavy.db"), splitFiles["spans.db"])
}

func mustCounts(t *testing.T, dir, layout string) map[string]int64 {
	t.Helper()
	c, err := rowCounts(context.Background(), dir, layout)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fileCounts counts rows in one data file opened on its own, without the
// reader's name resolution, so a count proves which file holds the rows.
func fileCounts(t *testing.T, path string, tables []string) map[string]int64 {
	t.Helper()
	db, err := repository.NewReadOnlyDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]int64{}
	for _, tbl := range tables {
		var n int64
		if err := db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatalf("%s %s: %v", path, tbl, err)
		}
		out[tbl] = n
	}
	return out
}

func assertNoTables(t *testing.T, path string, tables []string) {
	t.Helper()
	have, err := tableNames(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		for _, h := range have {
			if h == tbl {
				t.Errorf("%s holds table %s", filepath.Base(path), tbl)
			}
		}
	}
}

func TestContentionSpansAreOneTrace(t *testing.T) {
	g := newGenerator(prodProfile(), window{}, 1)
	spans := contSpans(g, time.Now(), contSpansPerTick)
	if len(spans) != contSpansPerTick {
		t.Fatalf("got %d spans, want %d", len(spans), contSpansPerTick)
	}
	roots := 0
	for _, s := range spans {
		if s.TraceID != spans[0].TraceID {
			t.Errorf("span %s in trace %s, want %s", s.SpanID, s.TraceID, spans[0].TraceID)
		}
		if s.ParentSpanID == "" {
			roots++
		} else if s.ParentSpanID != spans[0].SpanID {
			t.Errorf("span %s parent %s, want root %s", s.SpanID, s.ParentSpanID, spans[0].SpanID)
		}
	}
	if roots != 1 {
		t.Errorf("got %d roots, want 1", roots)
	}
}

func TestContentionBodySizes(t *testing.T) {
	g := newGenerator(prodProfile(), window{}, 1)
	if n := len(contLog(g, time.Now()).Body); n != contLogBodyBytes {
		t.Errorf("log body %d bytes, want %d", n, contLogBodyBytes)
	}
	p := contPrompt(g, time.Now())
	if len(p.PromptBody) != contPromptBytes || len(p.ResponseBody) != contPromptBytes {
		t.Errorf("prompt bodies %d/%d bytes, want %d", len(p.PromptBody), len(p.ResponseBody), contPromptBytes)
	}
	if got := contBatch(contMetricsPerTick, g, time.Now(), contMetric); len(got) != contMetricsPerTick {
		t.Errorf("metric batch %d rows, want %d", len(got), contMetricsPerTick)
	}
}

// The tick loop must stop when ctx ends and never call fn faster than the
// ticker allows.
func TestContentionTickLoopIsRateLimited(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	rec := &contRecorder{}
	calls := 0
	err := contTickLoop(ctx, 50*time.Millisecond, rec, func(time.Time) (int, error) {
		calls++
		return 2, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls > 6 || calls < 3 {
		t.Errorf("got %d calls in 300ms at 50ms ticks, want 3..6", calls)
	}
	lat, rows := rec.snapshot()
	if len(lat) != calls || rows != int64(2*calls) {
		t.Errorf("recorded %d batches, %d rows; want %d, %d", len(lat), rows, calls, 2*calls)
	}
}

func TestContentionIdleCellsHideWriteRows(t *testing.T) {
	idle := &contRun{reads: map[string]timings{"trace list": {time.Millisecond}}}
	for _, row := range contRows([]string{"trace list"}) {
		cell := contCell(row, idle)
		if row.write && cell != "-" {
			t.Errorf("%s on idle run = %q, want -", row.label, cell)
		}
		if !row.write && cell == "-" {
			t.Errorf("%s on idle run is empty", row.label)
		}
	}
}
