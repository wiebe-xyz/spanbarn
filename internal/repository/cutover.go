package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The cut-over moves the spans family of a single-file database into its own
// file. It runs in the writer at startup, after migrations and before any
// worker starts, so nothing writes to the span tables while they are copied:
// ingest waits in the write queue. Every writer deployment uses the Recreate
// strategy, so no other writer runs at that point.
//
// Steps, and what a stop after each one leaves:
//
//  1. Copy the family into cutoverPath and write a spans_cutover row. Main is
//     untouched; the next start removes the copy and starts over.
//  2. Drop the family's tables from main in one transaction. This is the
//     commit point: from here the copy is the only one, and an image that
//     predates the split can no longer write spans into main.
//  3. Rename the copy to SpansPath. A start that finds main without span
//     tables and a finished copy at cutoverPath renames it (finishCutover).
//
// A spans file next to a main that still holds span tables was never
// committed (a stop before step 2 under an earlier order of these steps, or
// a new install stopped mid-migration). The cut-over deletes it and copies
// again, because a writer may have written to main since.
const cutoverMarkerTable = "spans_cutover"

// cutoverBatchRows is the number of rows one INSERT copies. A variable so
// tests can cross batch boundaries with a handful of rows.
var cutoverBatchRows = 20000

func cutoverPath(dbPath string) string { return dbPath + ".spans-cutover" }

// volumeFreeBytes reports the bytes available to the writer on the volume
// holding path. A variable so tests can simulate a full volume.
var volumeFreeBytes = func(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// cutOver runs the cut-over when the options ask for it and reports whether
// the spans family now has its own file. A failure before the drop commits
// is logged and leaves the single-file layout in place, so the writer starts
// as before. An error is returned only after the drop: main then has no span
// tables, and the next start finishes the rename.
func (o StorageOptions) cutOver(ctx context.Context, main *sql.DB, dbPath string) (bool, error) {
	if !o.CutOver {
		return false, nil
	}
	log := o.logger()
	tmp := cutoverPath(dbPath)
	removeDBFiles(tmp)
	removeDBFiles(SpansPath(dbPath))
	if ok, err := cutoverHasRoom(dbPath, log); err != nil || !ok {
		if err != nil {
			log.Error("spans cut-over failed; keeping the single-file layout", "error", err)
		}
		return false, nil
	}

	start := time.Now()
	log.Info("spans cut-over: copying the spans family into its own file", "to", SpansPath(dbPath))
	rows, err := copySpansFamily(ctx, dbPath, tmp, o)
	if err != nil {
		removeDBFiles(tmp)
		log.Error("spans cut-over failed; keeping the single-file layout", "error", err)
		return false, nil
	}
	log.Info("spans cut-over: copy complete, dropping the span tables from main",
		"rows", rows, "elapsed", time.Since(start).Round(time.Millisecond))

	if err := dropMovedTables(ctx, main, true); err != nil {
		removeDBFiles(tmp)
		log.Error("spans cut-over failed; keeping the single-file layout", "error", err)
		return false, nil
	}
	if err := commitCutover(dbPath); err != nil {
		return false, fmt.Errorf("spans cut-over: %w", err)
	}
	log.Info("spans cut-over: done", "elapsed", time.Since(start).Round(time.Millisecond))
	return true, nil
}

// finishCutover completes a cut-over that stopped between the drop and the
// rename. Call it only when main has no span tables. A copy without the
// marker never finished and is removed.
func finishCutover(ctx context.Context, dbPath string, o StorageOptions) error {
	tmp := cutoverPath(dbPath)
	if !fileExists(tmp) {
		return nil
	}
	db, err := Open(tmp, OpenOptions{ReadOnly: true, CacheMB: o.CacheMB})
	if err != nil {
		return err
	}
	done, err := cutoverComplete(ctx, db.DB)
	db.Close()
	if err != nil {
		return err
	}
	if !done {
		removeDBFiles(tmp)
		return nil
	}
	o.logger().Info("spans cut-over: finishing an interrupted cut-over", "to", SpansPath(dbPath))
	return commitCutover(dbPath)
}

func commitCutover(dbPath string) error {
	tmp := cutoverPath(dbPath)
	if err := os.Rename(tmp, SpansPath(dbPath)); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	syncDir(filepath.Dir(dbPath))
	// The copy was checkpointed and closed, so its -wal is empty or gone;
	// what is left would be mistaken for the WAL of the next copy.
	removeDBFiles(tmp)
	return nil
}

// cutoverHasRoom checks that the volume can hold a second copy of the spans
// family. The family is a part of main, so main's size bounds it from above.
func cutoverHasRoom(dbPath string, log *slog.Logger) (bool, error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		return false, err
	}
	free, err := volumeFreeBytes(filepath.Dir(dbPath))
	if err != nil {
		return false, fmt.Errorf("measure free space: %w", err)
	}
	if free < info.Size() {
		log.Warn("spans cut-over postponed: the volume has less free space than the database file",
			"free_bytes", free, "db_bytes", info.Size())
		return false, nil
	}
	return true, nil
}

// copySpansFamily builds the spans file at tmp from main's span tables and
// returns the number of rows copied.
func copySpansFamily(ctx context.Context, dbPath, tmp string, o StorageOptions) (int64, error) {
	if err := createSpansFile(ctx, tmp, o); err != nil {
		return 0, err
	}
	dst, err := Open(tmp, OpenOptions{CacheMB: o.CacheMB, MmapMB: o.MmapMB, Attach: []Attachment{{Schema: CoreSchema, Path: dbPath}}})
	if err != nil {
		return 0, err
	}
	defer dst.Close()
	var total int64
	for _, table := range FamilySpans.Tables() {
		n, err := copyTable(ctx, dst.DB, table)
		if err != nil {
			return 0, fmt.Errorf("copy %s: %w", table, err)
		}
		total += n
		dst.checkpoint(ctx, 0, o.logger())
	}
	if err := copySequences(ctx, dst.DB); err != nil {
		return 0, err
	}
	if _, err := dst.ExecContext(ctx, `CREATE TABLE `+cutoverMarkerTable+` (completed_at TEXT NOT NULL, rows INTEGER NOT NULL)`); err != nil {
		return 0, err
	}
	if _, err := dst.ExecContext(ctx, `INSERT INTO `+cutoverMarkerTable+` VALUES (?, ?)`, time.Now().UTC().Format(time.RFC3339), total); err != nil {
		return 0, err
	}
	if dst.checkpoint(ctx, 0, o.logger()) != 0 {
		return 0, errors.New("final checkpoint of the copy did not complete")
	}
	return total, nil
}

// createSpansFile creates and migrates an empty spans file at path. It runs
// on a handle without main attached: goose names its version table
// unqualified, and a single-file main has a goose_spans_version of its own
// that would answer for the new file.
func createSpansFile(ctx context.Context, path string, o StorageOptions) error {
	db, err := Open(path, OpenOptions{CacheMB: o.CacheMB, MmapMB: o.MmapMB})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := enableIncrementalVacuum(ctx, db.DB); err != nil {
		return err
	}
	if err := MigrateSpans(ctx, db.DB); err != nil {
		return fmt.Errorf("spans migrations: %w", err)
	}
	return nil
}

// copyTable copies one table in rowid batches (one statement for a table
// without rowids) and checks the row counts match.
func copyTable(ctx context.Context, dst *sql.DB, table string) (int64, error) {
	cols, err := storedColumns(ctx, dst, table)
	if err != nil {
		return 0, err
	}
	list := strings.Join(cols, ", ")
	insert := `INSERT INTO main.` + table + ` (` + list + `) SELECT ` + list + ` FROM ` + CoreSchema + `.` + table
	withRowid, err := hasRowid(ctx, dst, table)
	if err != nil {
		return 0, err
	}
	if !withRowid {
		if _, err := dst.ExecContext(ctx, insert); err != nil {
			return 0, err
		}
	} else if err := copyRowidBatches(ctx, dst, table, insert); err != nil {
		return 0, err
	}
	var src, got int64
	if err := dst.QueryRowContext(ctx, `SELECT count(*) FROM `+CoreSchema+`.`+table).Scan(&src); err != nil {
		return 0, err
	}
	if err := dst.QueryRowContext(ctx, `SELECT count(*) FROM main.`+table).Scan(&got); err != nil {
		return 0, err
	}
	if src != got {
		return 0, fmt.Errorf("copied %d rows, source has %d", got, src)
	}
	return got, nil
}

func copyRowidBatches(ctx context.Context, dst *sql.DB, table, insert string) error {
	var last int64
	for {
		var hi sql.NullInt64
		if err := dst.QueryRowContext(ctx, `SELECT max(rowid) FROM (SELECT rowid FROM `+CoreSchema+`.`+table+
			` WHERE rowid > ? ORDER BY rowid LIMIT ?)`, last, cutoverBatchRows).Scan(&hi); err != nil {
			return err
		}
		if !hi.Valid {
			return nil
		}
		if _, err := dst.ExecContext(ctx, insert+` WHERE rowid > ? AND rowid <= ?`, last, hi.Int64); err != nil {
			return err
		}
		last = hi.Int64
	}
}

// storedColumns lists table's columns minus generated ones, which SQLite
// computes and refuses as insert targets.
func storedColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_xinfo(?, 'main') WHERE hidden = 0 ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, `"`+c+`"`)
	}
	if len(cols) == 0 && rows.Err() == nil {
		return nil, fmt.Errorf("table %s has no columns", table)
	}
	return cols, rows.Err()
}

func hasRowid(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var ddl string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM main.sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&ddl); err != nil {
		return false, err
	}
	return !strings.Contains(strings.ToUpper(ddl), "WITHOUT ROWID"), nil
}

// copySequences carries main's AUTOINCREMENT counters over, so ids keep
// growing from where main left them even when its newest rows were deleted.
// Code that walks span ids with a watermark relies on that.
func copySequences(ctx context.Context, dst *sql.DB) error {
	tables := FamilySpans.Tables()
	args := make([]any, len(tables))
	for i, t := range tables {
		args[i] = t
	}
	in := placeholderList(len(tables))
	if _, err := dst.ExecContext(ctx, `DELETE FROM main.sqlite_sequence WHERE name IN (`+in+`)`, args...); err != nil {
		return fmt.Errorf("copy sequences: %w", err)
	}
	if _, err := dst.ExecContext(ctx, `INSERT INTO main.sqlite_sequence (name, seq)
		SELECT name, seq FROM `+CoreSchema+`.sqlite_sequence WHERE name IN (`+in+`)`, args...); err != nil {
		return fmt.Errorf("copy sequences: %w", err)
	}
	return nil
}

// cutoverComplete reports whether the spans file behind db was written by a
// finished cut-over.
func cutoverComplete(ctx context.Context, db *sql.DB) (bool, error) {
	return hasTable(ctx, db, cutoverMarkerTable)
}

func removeDBFiles(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

// syncDir flushes a rename to disk. Best effort: not every platform can open
// a directory for fsync.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
