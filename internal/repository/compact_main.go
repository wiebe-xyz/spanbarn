package repository

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// autoVacuumIncremental is PRAGMA auto_vacuum's value for INCREMENTAL.
const autoVacuumIncremental = 2

// compactMain rewrites main once in auto_vacuum=INCREMENTAL mode, the
// procedure deploy/docs/compact-main.md describes, in place at writer start.
//
// Migration 018 asks for INCREMENTAL, but SQLite only applies that to a file
// with no tables or through a VACUUM, so a main created before it stays NONE:
// row retention and the shard cut-over free pages that never leave the file.
// On staging that was 11 GB of freelist around 290 MB of live data. After
// this runs, the checkpoint loop's incremental_vacuum returns freed pages,
// including those of main tables the writer drops later.
//
// It does nothing on a file that is already INCREMENTAL, and skips when main's
// live data exceeds maxLiveBytes: VACUUM holds main's write lock for as long as
// it runs. db must be main's own handle with nothing attached, before any
// worker uses it. In WAL mode readers on other handles keep reading their
// snapshot during the rewrite.
func compactMain(ctx context.Context, db *sql.DB, maxLiveBytes int64, log *slog.Logger) error {
	before, err := mainPages(ctx, db)
	if err != nil {
		return err
	}
	if before.autoVacuum == autoVacuumIncremental {
		return nil
	}
	live := before.liveBytes()
	if maxLiveBytes > 0 && live > maxLiveBytes {
		log.Warn("main compaction skipped: live data above the limit",
			"live_bytes", live, "limit_bytes", maxLiveBytes, "file_bytes", before.fileBytes())
		return nil
	}
	if _, err := db.ExecContext(ctx, `PRAGMA main.wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint before compaction: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA main.auto_vacuum = INCREMENTAL`); err != nil {
		return fmt.Errorf("set auto_vacuum: %w", err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM main`); err != nil {
		return fmt.Errorf("vacuum main: %w", err)
	}
	// In WAL mode VACUUM writes through the WAL; the file shrinks when a
	// checkpoint copies the pages back.
	if _, err := db.ExecContext(ctx, `PRAGMA main.wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint after compaction: %w", err)
	}
	after, err := mainPages(ctx, db)
	if err != nil {
		return err
	}
	log.Info("main compacted", "bytes_before", before.fileBytes(), "bytes_after", after.fileBytes(),
		"live_bytes", live, "auto_vacuum", after.autoVacuum)
	return nil
}

type pageCounts struct {
	autoVacuum, pageSize, pages, free int64
}

func (p pageCounts) liveBytes() int64 { return (p.pages - p.free) * p.pageSize }
func (p pageCounts) fileBytes() int64 { return p.pages * p.pageSize }

func mainPages(ctx context.Context, db *sql.DB) (pageCounts, error) {
	var p pageCounts
	for _, q := range []struct {
		pragma string
		dst    *int64
	}{
		{"auto_vacuum", &p.autoVacuum},
		{"page_size", &p.pageSize},
		{"page_count", &p.pages},
		{"freelist_count", &p.free},
	} {
		if err := db.QueryRowContext(ctx, `PRAGMA main.`+q.pragma).Scan(q.dst); err != nil {
			return p, fmt.Errorf("read %s: %w", q.pragma, err)
		}
	}
	return p, nil
}
