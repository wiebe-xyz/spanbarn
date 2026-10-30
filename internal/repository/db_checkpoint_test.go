package repository

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCheckpoint exercises a TRUNCATE checkpoint against a file-backed DB
// (WAL mode is a no-op on :memory:). It must complete without error, leave
// committed data readable, and reset the WAL file to zero bytes.
func TestCheckpoint(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	path := filepath.Join(t.TempDir(), "cp.db")
	db, err := NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)

	// Generate WAL frames. wal_autocheckpoint(0) means nothing is folded
	// into the main DB until we checkpoint explicitly.
	for i := 0; i < 20; i++ {
		if _, err := repo.CreateProject(fmt.Sprintf("p%d", i), "x"); err != nil {
			t.Fatalf("CreateProject %d: %v", i, err)
		}
	}
	walPath := path + "-wal"
	if fi, err := os.Stat(walPath); err != nil || fi.Size() == 0 {
		t.Fatalf("expected a non-empty WAL before checkpoint (err=%v)", err)
	}

	db.FinalCheckpoint(discard)

	// Committed data must survive the checkpoint.
	got, err := repo.GetProjectBySlug("p19")
	if err != nil {
		t.Fatalf("GetProjectBySlug after checkpoint: %v", err)
	}
	if got.Slug != "p19" {
		t.Fatalf("unexpected project after checkpoint: %+v", got)
	}

	fi, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat WAL after checkpoint: %v", err)
	}
	if fi.Size() != 0 {
		t.Fatalf("TRUNCATE should reset the WAL to 0 bytes, got %d", fi.Size())
	}
}

// TestCheckpointFrameCount verifies checkpoint() reports 0 WAL frames after a
// TRUNCATE reset.
func TestCheckpointFrameCount(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "cp.db")
	db, err := NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)
	for i := 0; i < 50; i++ {
		if _, err := repo.CreateProject(fmt.Sprintf("p%d", i), "x"); err != nil {
			t.Fatalf("CreateProject %d: %v", i, err)
		}
	}
	ctx := context.Background()

	if n := db.checkpoint(ctx, 0, discard); n != 0 {
		t.Fatalf("TRUNCATE checkpoint should report 0 WAL frames after reset, got %d", n)
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
	if err := MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := NewRepository(db.DB).CreateProject("p", "x"); err != nil {
		t.Fatalf("CreateProject: %v", err)
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

// TestCheckpointDoesNotWaitForReader holds a read snapshot open while the
// writer adds WAL frames, the way a reader pod or the settings snapshot does.
// The checkpoint must report the WAL as busy at once instead of holding the
// single writer connection in SQLite's busy handler, and a write right after it
// must not queue behind it (#337). Once the reader lets go, the next
// checkpoint resets the WAL.
func TestCheckpointDoesNotWaitForReader(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "cp.db")
	db, err := NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := MigrateSingleFile(ctx, db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)
	if _, err := repo.CreateProject("before", "x"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	reader, err := NewReadOnlyDB(path)
	if err != nil {
		t.Fatalf("NewReadOnlyDB: %v", err)
	}
	t.Cleanup(func() { reader.Close() })
	tx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin read tx: %v", err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM projects").Scan(&n); err != nil {
		t.Fatalf("read inside snapshot: %v", err)
	}

	// Frames past the reader's snapshot, which the checkpoint cannot reset.
	for i := 0; i < 5; i++ {
		if _, err := repo.CreateProject(fmt.Sprintf("after%d", i), "x"); err != nil {
			t.Fatalf("CreateProject %d: %v", i, err)
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	frames := db.checkpoint(cctx, 0, discard)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("checkpoint waited %s for the reader; it must return busy at once", took)
	}
	if frames <= 0 {
		t.Fatalf("checkpoint with a reader holding the WAL reported %d frames, want the WAL still in use", frames)
	}

	start = time.Now()
	if _, err := repo.CreateProject("write-after-checkpoint", "x"); err != nil {
		t.Fatalf("write after checkpoint: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("write after a busy checkpoint took %s", took)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("end read tx: %v", err)
	}
	if n := db.checkpoint(ctx, 0, discard); n != 0 {
		t.Fatalf("checkpoint after the reader released reported %d frames, want 0", n)
	}
}

// TestBackfillLeavesTheWriterFree holds a write transaction open on the
// handle's only writer connection, the way a slow write does, and requires the
// backfill to copy the whole WAL anyway. The backfill is where the bulk of the
// checkpoint's copy and fsync happens, so the writer must not wait for it
// (SPA-74, #360).
func TestBackfillLeavesTheWriterFree(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "cp.db")
	db, err := NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := MigrateSingleFile(ctx, db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)
	for i := 0; i < 20; i++ {
		if _, err := repo.CreateProject(fmt.Sprintf("p%d", i), "x"); err != nil {
			t.Fatalf("CreateProject %d: %v", i, err)
		}
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("hold the writer: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin write tx: %v", err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK") //nolint:errcheck
	done := make(chan int, 1)
	go func() { done <- db.backfill(ctx, discard) }()
	select {
	case left := <-done:
		if left != 0 {
			t.Errorf("backfill left %d frames, want the WAL copied", left)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backfill is still waiting while a write transaction holds the writer")
	}
}

// TestPeriodicCheckpointBoundsTheWALUnderAScanningReader runs the periodic
// checkpoint while a writer commits without pause and a second handle scans
// the file, the way the writer and the reader pod share production's files.
// The WAL file must shrink again tick after tick. A PASSIVE checkpoint alone
// never catches up with the writer, so the WAL file only grows: production's
// spans WAL went from 57 MB to 283 MB in 22 minutes (#368, reverted in #369).
func TestPeriodicCheckpointBoundsTheWALUnderAScanningReader(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "cp.db")
	db, err := NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := MigrateSingleFile(ctx, db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)
	reader, err := NewReadOnlyDB(path)
	if err != nil {
		t.Fatalf("NewReadOnlyDB: %v", err)
	}
	t.Cleanup(func() { reader.Close() })

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ctx.Err() == nil; i++ {
			if _, err := repo.CreateProject(fmt.Sprintf("w%d", i), "x"); err != nil && ctx.Err() == nil {
				errs <- fmt.Errorf("write %d: %w", i, err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			tx, err := reader.BeginTx(ctx, nil)
			if err != nil {
				if ctx.Err() == nil {
					errs <- fmt.Errorf("begin scan: %w", err)
				}
				return
			}
			var n int
			err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&n)
			time.Sleep(5 * time.Millisecond)
			_ = tx.Rollback()
			if err != nil && ctx.Err() == nil {
				errs <- fmt.Errorf("scan: %w", err)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	walSize := func() int64 {
		fi, err := os.Stat(path + "-wal")
		if err != nil {
			return 0
		}
		return fi.Size()
	}

	const ticks = 10
	shrank := 0
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < ticks; i++ {
		before := walSize()
		tctx, cancel := context.WithTimeout(ctx, time.Second)
		db.periodicCheckpoint(tctx, 5*time.Millisecond, discard)
		cancel()
		if walSize() < before {
			shrank++
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if shrank < ticks/2 {
		t.Errorf("the WAL file shrank in %d of %d checkpoint ticks, want at least %d", shrank, ticks, ticks/2)
	}
}
