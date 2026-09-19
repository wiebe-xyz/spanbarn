package spool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A record larger than the old 1 MiB scanner buffer must replay and advance the
// cursor. Before, bufio.Scanner returned ErrTooLong, the forwarder retried the
// same offset forever, and everything behind it in the spool stayed stuck.
func TestSpoolReadsRecordLargerThanOneMiB(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewSpool(dir, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	recs := makeRecords(3)
	recs[1].Name = strings.Repeat("x", 3<<20)
	if err := sp.Write(recs); err != nil {
		t.Fatal(err)
	}

	got, cursor, err := sp.Read(0, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Read returned %d records, want 3", len(got))
	}
	if len(got[1].Name) != 3<<20 {
		t.Errorf("large record name length = %d, want %d", len(got[1].Name), 3<<20)
	}
	if size := sp.Size(); cursor != size {
		t.Errorf("cursor = %d, want end of file %d", cursor, size)
	}
}

// The cursor must land exactly on a line boundary so a follow-up Read from it
// returns the next record and nothing else.
func TestSpoolCursorIsExactAcrossLimitedReads(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewSpool(dir, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	recs := makeRecords(5)
	recs[2].Name = strings.Repeat("y", 2<<20)
	if err := sp.Write(recs); err != nil {
		t.Fatal(err)
	}

	var seen []string
	var cursor int64
	for {
		got, next, err := sp.Read(cursor, 2)
		if err != nil {
			t.Fatalf("Read(%d): %v", cursor, err)
		}
		if len(got) == 0 {
			break
		}
		for _, r := range got {
			seen = append(seen, r.SpanID)
		}
		cursor = next
	}

	want := []string{"span-0", "span-1", "span-2", "span-3", "span-4"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("replayed %v, want %v", seen, want)
	}
}

// A line with no trailing newline is a write still in flight. It must not be
// consumed, or the cursor would skip past bytes the writer has not finished.
func TestSpoolLeavesUnterminatedTailUnread(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewSpool(dir, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	if err := sp.Write(makeRecords(2)); err != nil {
		t.Fatal(err)
	}
	complete := sp.Size()

	f, err := os.OpenFile(filepath.Join(dir, spoolFileName), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"project_id":1,"trace_id":"t","span_id":"partial"`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, cursor, err := sp.Read(0, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Read returned %d records, want 2", len(got))
	}
	if cursor != complete {
		t.Errorf("cursor = %d, want %d (end of the last complete line)", cursor, complete)
	}
}
