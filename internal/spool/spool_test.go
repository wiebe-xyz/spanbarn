package spool

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func makeRecords(n int) []model.SpanRecord {
	records := make([]model.SpanRecord, n)
	for i := 0; i < n; i++ {
		records[i] = model.SpanRecord{
			ProjectID:   1,
			TraceID:     fmt.Sprintf("trace-%d", i),
			SpanID:      fmt.Sprintf("span-%d", i),
			Name:        fmt.Sprintf("op-%d", i),
			Service:     "test-svc",
			Kind:        "SERVER",
			Status:      "OK",
			StartTimeUs: int64(i * 1000),
			DurationUs:  500,
			Attributes:  json.RawMessage(`{"key":"value"}`),
		}
	}
	return records
}

func TestSpoolWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewSpool(dir, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	records := makeRecords(10)
	if err := sp.Write(records); err != nil {
		t.Fatal(err)
	}

	got, nextCursor, err := sp.Read(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("Read() returned %d records, want 10", len(got))
	}
	if nextCursor <= 0 {
		t.Fatalf("nextCursor = %d, expected > 0", nextCursor)
	}

	for i, rec := range got {
		want := fmt.Sprintf("span-%d", i)
		if rec.SpanID != want {
			t.Errorf("records[%d].SpanID = %q, want %q", i, rec.SpanID, want)
		}
	}
}

func TestSpoolReadWithLimit(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewSpool(dir, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	if err := sp.Write(makeRecords(20)); err != nil {
		t.Fatal(err)
	}

	// Read first 5.
	batch1, cursor1, err := sp.Read(0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch1) != 5 {
		t.Fatalf("batch1: got %d records, want 5", len(batch1))
	}

	// Read next 5 from cursor.
	batch2, _, err := sp.Read(cursor1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch2) != 5 {
		t.Fatalf("batch2: got %d records, want 5", len(batch2))
	}
	if batch2[0].SpanID != "span-5" {
		t.Errorf("batch2[0].SpanID = %q, want %q", batch2[0].SpanID, "span-5")
	}
}

func TestSpoolCursor(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewSpool(dir, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	// Save and reload cursor.
	if err := sp.SaveCursor(12345); err != nil {
		t.Fatal(err)
	}

	pos, err := sp.LoadCursor()
	if err != nil {
		t.Fatal(err)
	}
	if pos != 12345 {
		t.Fatalf("LoadCursor() = %d, want 12345", pos)
	}

	// LoadCursor on a fresh dir returns 0.
	dir2 := t.TempDir()
	sp2, err := NewSpool(dir2, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp2.Close()

	pos2, err := sp2.LoadCursor()
	if err != nil {
		t.Fatal(err)
	}
	if pos2 != 0 {
		t.Fatalf("LoadCursor() on new dir = %d, want 0", pos2)
	}
}

func TestSpoolRotation(t *testing.T) {
	dir := t.TempDir()
	// Set a very small max to trigger rotation quickly.
	sp, err := NewSpool(dir, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	// Write enough records to exceed 500 bytes and trigger rotation.
	for batch := 0; batch < 5; batch++ {
		recs := makeRecords(5)
		for i := range recs {
			recs[i].SpanID = fmt.Sprintf("span-b%d-s%d", batch, i)
		}
		if err := sp.Write(recs); err != nil {
			t.Fatal(err)
		}
	}

	// The .old file should exist (rotation happened).
	oldPath := dir + "/spool.ndjson.old"
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		t.Fatal("expected spool.ndjson.old to exist after rotation")
	}

	// Now write one more small batch. These should be readable from cursor 0 of the current file.
	finalRecs := []model.SpanRecord{{
		ProjectID: 1,
		SpanID:    "final-span",
		Name:      "final-op",
		Service:   "test-svc",
		Kind:      "SERVER",
		Status:    "OK",
	}}
	if err := sp.Write(finalRecs); err != nil {
		t.Fatal(err)
	}

	got, nextCursor, err := sp.Read(0, 1000)
	if err != nil {
		t.Fatal(err)
	}

	// We should get at least the final record from the current file.
	if len(got) == 0 {
		t.Fatal("Read() returned 0 records after rotation, expected at least 1")
	}
	t.Logf("read %d records from current spool, nextCursor=%d", len(got), nextCursor)

	// Verify the final record is present.
	found := false
	for _, r := range got {
		if r.SpanID == "final-span" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find 'final-span' in current spool after rotation")
	}
}

func TestSpoolReadAfterRotation_CursorResets(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewSpool(dir, 100000)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	recs := makeRecords(5)
	if err := sp.Write(recs); err != nil {
		t.Fatal(err)
	}

	got, cursor, err := sp.Read(0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 records, got %d", len(got))
	}

	// Simulate rotation: rename current to .old, create new file.
	sp.Close()
	curPath := dir + "/spool.ndjson"
	if err := os.Rename(curPath, dir+"/spool.ndjson.old"); err != nil {
		t.Fatal(err)
	}
	sp, err = NewSpool(dir, 100000)
	if err != nil {
		t.Fatal(err)
	}

	newRecs := makeRecords(3)
	for i := range newRecs {
		newRecs[i].SpanID = fmt.Sprintf("new-%d", i)
	}
	if err := sp.Write(newRecs); err != nil {
		t.Fatal(err)
	}

	// cursor exceeds new file size → should reset to 0 and read new file.
	got2, _, err := sp.Read(cursor, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 3 {
		t.Fatalf("expected 3 records from new file after cursor reset, got %d", len(got2))
	}
	if got2[0].SpanID != "new-0" {
		t.Errorf("got2[0].SpanID = %q, want %q", got2[0].SpanID, "new-0")
	}
}

func TestSpoolConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewSpool(dir, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	const goroutines = 8
	const perG = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				rec := model.SpanRecord{
					ProjectID: 1,
					SpanID:    fmt.Sprintf("g%d-s%d", id, i),
					Name:      "concurrent-op",
					Service:   "test",
					Kind:      "SERVER",
					Status:    "OK",
				}
				if err := sp.Write([]model.SpanRecord{rec}); err != nil {
					t.Errorf("Write failed: %v", err)
				}
			}
		}(g)
	}
	wg.Wait()

	got, _, err := sp.Read(0, goroutines*perG+100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != goroutines*perG {
		t.Fatalf("Read() returned %d records, want %d", len(got), goroutines*perG)
	}
}
