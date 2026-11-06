package spool

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &logs
}

func appendTornLine(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("{\"trace_id\":\"torn\n"); err != nil {
		t.Fatal(err)
	}
}

// A failed publish reads the same lines again, so a skipped line counts when the
// reader moves past it and once only.
func TestUndecodableLineCountsOnceAcrossRereads(t *testing.T) {
	logs := captureDefaultLog(t)
	dir := t.TempDir()
	sp, err := NewSpool(dir, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	if err := sp.Write(makeRecords(1)); err != nil {
		t.Fatal(err)
	}
	appendTornLine(t, filepath.Join(dir, spoolFileName))
	if err := sp.Write(makeRecords(1)); err != nil {
		t.Fatal(err)
	}

	var next int64
	for i := 0; i < 3; i++ {
		recs, n, err := sp.Read(0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 2 {
			t.Fatalf("read %d records, want 2", len(recs))
		}
		next = n
	}
	if got := sp.Undecodable(); got != 0 {
		t.Errorf("Undecodable before the cursor moved = %d, want 0", got)
	}
	if err := sp.SaveCursor(next); err != nil {
		t.Fatal(err)
	}
	if got := sp.Undecodable(); got != 1 {
		t.Errorf("Undecodable = %d, want 1", got)
	}
	if !strings.Contains(logs.String(), "lines=1 total=1") {
		t.Errorf("no ERROR line for the skipped line:\n%s", logs.String())
	}
}

// A damaged file can hold many such lines, so the ERROR carries a total. What
// waits when the pod stops goes out with Close.
func TestUndecodableLogIsCapped(t *testing.T) {
	logs := captureDefaultLog(t)
	dir := t.TempDir()
	sp, err := NewSpool(dir, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, spoolFileName)

	cursor := int64(0)
	for i := 0; i < 5; i++ {
		appendTornLine(t, path)
		if err := sp.Write(makeRecords(1)); err != nil {
			t.Fatal(err)
		}
		_, next, err := sp.Read(cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		if err := sp.SaveCursor(next); err != nil {
			t.Fatal(err)
		}
		cursor = next
	}
	if got := strings.Count(logs.String(), "level=ERROR"); got != 1 {
		t.Fatalf("ERROR lines within the interval = %d, want 1:\n%s", got, logs.String())
	}

	sp.mu.Lock()
	sp.lastLinesLog = time.Now().Add(-undecodableLogInterval)
	sp.mu.Unlock()
	appendTornLine(t, path)
	if err := sp.Write(makeRecords(1)); err != nil {
		t.Fatal(err)
	}
	_, next, err := sp.Read(cursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.SaveCursor(next); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "lines=5 total=6") {
		t.Errorf("second line does not hold the total since the first:\n%s", logs.String())
	}

	logs.Reset()
	appendTornLine(t, path)
	if err := sp.Write(makeRecords(1)); err != nil {
		t.Fatal(err)
	}
	if _, next, err = sp.Read(next, 10); err != nil {
		t.Fatal(err)
	}
	if err := sp.SaveCursor(next); err != nil {
		t.Fatal(err)
	}
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "lines=1 total=7") {
		t.Errorf("Close did not report what waited:\n%s", logs.String())
	}
}

// A rotated file ending in a torn line is removed with no batch after it, so its
// count cannot wait for a cursor save.
func TestUndecodableTailOfARotatedFileCounts(t *testing.T) {
	captureDefaultLog(t)
	dir := t.TempDir()
	sp, err := NewSpool(dir, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	old := filepath.Join(dir, oldFileName)
	if err := os.WriteFile(old, []byte("{\"torn\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	recs, _, err := sp.Read(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("read %d records, want 0", len(recs))
	}
	if got := sp.Undecodable(); got != 1 {
		t.Errorf("Undecodable = %d, want 1", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("rotated file still there: %v", err)
	}
}
