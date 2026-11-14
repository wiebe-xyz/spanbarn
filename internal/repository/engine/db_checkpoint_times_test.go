package engine

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCheckpointTimesLogOnlySlowCheckpoints checks the line is written at
// slowCheckpoint, that waiting for the connection does not count towards it,
// and that the line names the file and each phase.
func TestCheckpointTimesLogOnlySlowCheckpoints(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	checkpointTimes{truncate: slowCheckpoint - 1, connWait: time.Hour}.log(log, "/data/x.db", 0)
	if buf.Len() != 0 {
		t.Fatalf("a checkpoint under the line logged: %s", buf.String())
	}

	checkpointTimes{backfill: 200 * time.Millisecond, truncate: 250 * time.Millisecond, vacuum: 50 * time.Millisecond, attempts: 2}.log(log, "/data/x.db", 7)
	line := buf.String()
	for _, want := range []string{"slow wal checkpoint", "file=x.db", "backfill_ms=200", "truncate_ms=250", "vacuum_ms=50", "attempts=2", "wal_frames=7"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q lacks %q", line, want)
		}
	}
}

// TestCheckpointTimesCoverEveryPhase checks that a periodic checkpoint reports
// the time of each attempt's backfill and TRUNCATE.
func TestCheckpointTimesCoverEveryPhase(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := NewDB(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// A write leaves frames in the WAL for the checkpoint to reset.
	if _, err := db.Exec("CREATE TABLE t (x); INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("write: %v", err)
	}

	backfilled := false
	frames, times := db.checkpointWith(context.Background(), 0, discard, func() {
		backfilled = true
		time.Sleep(time.Millisecond)
	})
	if frames != 0 || !backfilled {
		t.Fatalf("frames = %d, backfilled = %v", frames, backfilled)
	}
	if times.attempts != 1 || times.backfill < time.Millisecond || times.truncate <= 0 || times.vacuum <= 0 {
		t.Fatalf("times = %+v", times)
	}
}
