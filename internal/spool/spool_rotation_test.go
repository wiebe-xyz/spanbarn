package spool

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func namedRecords(prefix string, n int) []model.SpanRecord {
	recs := makeRecords(n)
	for i := range recs {
		recs[i].SpanID = fmt.Sprintf("%s-%d", prefix, i)
	}
	return recs
}

// drain reads from cursor the way the forwarder does, until nothing is left.
func drain(t *testing.T, sp *Spool, cursor int64) ([]string, int64) {
	t.Helper()
	var ids []string
	for {
		recs, next, err := sp.Read(cursor, 3)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(recs) == 0 {
			return ids, cursor
		}
		for _, r := range recs {
			ids = append(ids, r.SpanID)
		}
		cursor = next
	}
}

func wantIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("read %v, want %v", got, want)
	}
}

// The forwarder only follows the current file, so what it had not read when the
// file rotated was gone, always including the batch that triggered the rotation.
func TestSpoolReadsWhatRotationLeftUnread(t *testing.T) {
	sp, err := NewSpool(t.TempDir(), 1500)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	if err := sp.Write(namedRecords("a", 2)); err != nil {
		t.Fatal(err)
	}
	got, cursor := drain(t, sp, 0)
	wantIDs(t, got, "a-0", "a-1")

	// Unread when the rotation comes, and the batch that triggers it.
	if err := sp.Write(namedRecords("b", 1)); err != nil {
		t.Fatal(err)
	}
	if err := sp.Write(namedRecords("c", 6)); err != nil {
		t.Fatal(err)
	}
	if sp.Size() != 0 {
		t.Fatalf("size = %d, want a rotation to have happened", sp.Size())
	}
	if err := sp.Write(namedRecords("d", 1)); err != nil {
		t.Fatal(err)
	}

	got, _ = drain(t, sp, cursor)
	wantIDs(t, got, "b-0", "c-0", "c-1", "c-2", "c-3", "c-4", "c-5", "d-0")
}

// The forwarder keeps its cursor when a read returns nothing, so the read that
// removes the rotated file leaves it holding an offset from that file. That offset
// is past the end of the new file, which is what sends the next read to its start.
func TestSpoolCursorKeptFromARotatedFileStillReadsTheNewFileFromItsStart(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewSpool(dir, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	if err := sp.Write(namedRecords("a", 5)); err != nil {
		t.Fatal(err)
	}
	if sp.Size() != 0 {
		t.Fatalf("size = %d, want a rotation to have happened", sp.Size())
	}
	got, cursor := drain(t, sp, 0)
	wantIDs(t, got, "a-0", "a-1", "a-2", "a-3", "a-4")
	if _, err := os.Stat(filepath.Join(dir, oldFileName)); !os.IsNotExist(err) {
		t.Fatalf("read old file still there: %v", err)
	}

	if err := sp.Write(namedRecords("b", 1)); err != nil {
		t.Fatal(err)
	}
	got, _ = drain(t, sp, cursor)
	wantIDs(t, got, "b-0")
}

// A rotated file goes away once it is read. Its presence is what says it still
// holds unread records, so a restart carries on where the cursor stopped.
func TestSpoolRestartWithAnOldFileCarriesOnFromTheCursor(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewSpool(dir, 500)
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.Write(namedRecords("a", 2)); err != nil {
		t.Fatal(err)
	}
	got, cursor := drain(t, sp, 0)
	wantIDs(t, got, "a-0", "a-1")
	if err := sp.SaveCursor(cursor); err != nil {
		t.Fatal(err)
	}
	if err := sp.Write(namedRecords("b", 5)); err != nil {
		t.Fatal(err)
	}
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}

	sp, err = NewSpool(dir, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	if err := sp.Write(namedRecords("c", 1)); err != nil {
		t.Fatal(err)
	}
	cursor, err = sp.LoadCursor()
	if err != nil {
		t.Fatal(err)
	}

	got, cursor = drain(t, sp, cursor)
	wantIDs(t, got, "b-0", "b-1", "b-2", "b-3", "b-4", "c-0")
	if _, err := os.Stat(filepath.Join(dir, oldFileName)); !os.IsNotExist(err) {
		t.Errorf("read old file still there: %v", err)
	}

	again, _ := drain(t, sp, cursor)
	wantIDs(t, again)
}

// Disk stays at two files, so a reader a whole file behind loses records. That
// has to show as an ERROR, and the file the cursor points into has to survive.
func TestSpoolRotatingOverAnUnreadFileLogsTheLossAndKeepsTheFileTheCursorIsIn(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	sp, err := NewSpool(t.TempDir(), 500)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	if err := sp.Write(namedRecords("a", 1)); err != nil {
		t.Fatal(err)
	}
	_, cursor := drain(t, sp, 0)

	if err := sp.Write(namedRecords("b", 5)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("first rotation logged a loss:\n%s", logs.String())
	}
	if err := sp.Write(namedRecords("lost", 5)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("loss not logged at ERROR:\n%s", logs.String())
	}
	if err := sp.Write(namedRecords("c", 1)); err != nil {
		t.Fatal(err)
	}

	got, _ := drain(t, sp, cursor)
	wantIDs(t, got, "b-0", "b-1", "b-2", "b-3", "b-4", "c-0")
}
