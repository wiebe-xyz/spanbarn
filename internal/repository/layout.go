package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
)

// Attached schema names. A handle on the main file sees the spans file as
// SpansSchema; a handle on the spans file sees the main file as CoreSchema.
// Queries use unqualified table names and rely on SQLite resolving them in
// main first, then in each attached database in attach order.
const (
	SpansSchema = "fam_spans"
	CoreSchema  = "fam_core"
)

// SpansPath returns the file that holds the spans family for the main
// database at dbPath.
func SpansPath(dbPath string) string { return dbPath + ".spans" }

// Storage is the set of writable handles behind one SPANBARN_DB_PATH.
//
// In the split layout the spans family lives in SpansPath(path) with its own
// handle, and each handle attaches the other file read-only. In the single-file
// layout (databases created before the split, until the cut-over moves their
// spans) Spans is nil and every family writes through Main.
type Storage struct {
	Main  *DB
	Spans *DB
}

// StorageOptions sizes the handles of a Storage. CacheMB and MmapMB apply to
// each file's own handle; AttachCacheMB and AttachMmapMB to the other file as
// seen through an attachment, which only serves reads.
type StorageOptions struct {
	CacheMB       int
	MmapMB        int
	AttachCacheMB int
	AttachMmapMB  int
}

// OpenStorage opens the database at dbPath for writing, runs both migration
// tracks and returns its handles.
//
// The layout is decided once, before any migration: a database whose spans
// file exists, or whose main file has no spans table yet (a new install, or a
// restored settings snapshot), uses the split layout. A main file that already
// holds a spans table keeps the single-file layout.
func OpenStorage(ctx context.Context, dbPath string, o StorageOptions) (*Storage, error) {
	plain, err := Open(dbPath, OpenOptions{CacheMB: o.CacheMB, MmapMB: o.MmapMB})
	if err != nil {
		return nil, err
	}
	split, err := splitLayoutRW(ctx, plain.DB, dbPath)
	if err != nil {
		plain.Close()
		return nil, err
	}
	if !split {
		if err := migrateSingleFile(ctx, plain.DB); err != nil {
			plain.Close()
			return nil, err
		}
		return &Storage{Main: plain}, nil
	}
	err = prepareSplit(ctx, plain.DB, dbPath, o)
	plain.Close()
	if err != nil {
		return nil, err
	}
	return openSplit(dbPath, o)
}

func migrateSingleFile(ctx context.Context, db *sql.DB) error {
	if err := Migrate(db); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	if err := MigrateSpans(ctx, db); err != nil {
		return fmt.Errorf("run spans migrations: %w", err)
	}
	return nil
}

// prepareSplit creates and migrates the spans file before main is migrated, so
// a new install never has a moment where main holds span tables and the spans
// file is missing (a reader starting then would pick the single-file layout).
// Then it migrates main and drops the span tables main's migrations created.
func prepareSplit(ctx context.Context, main *sql.DB, dbPath string, o StorageOptions) error {
	spans, err := Open(SpansPath(dbPath), OpenOptions{CacheMB: o.CacheMB, MmapMB: o.MmapMB})
	if err != nil {
		return err
	}
	defer spans.Close()
	if err := enableIncrementalVacuum(ctx, spans.DB); err != nil {
		return fmt.Errorf("spans file auto_vacuum: %w", err)
	}
	if err := MigrateSpans(ctx, spans.DB); err != nil {
		return fmt.Errorf("run spans migrations: %w", err)
	}
	if err := Migrate(main); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return dropMovedTables(ctx, main)
}

func openSplit(dbPath string, o StorageOptions) (*Storage, error) {
	main, err := Open(dbPath, OpenOptions{CacheMB: o.CacheMB, MmapMB: o.MmapMB, Attach: []Attachment{
		{Schema: SpansSchema, Path: SpansPath(dbPath), CacheMB: o.AttachCacheMB, MmapMB: o.AttachMmapMB},
	}})
	if err != nil {
		return nil, err
	}
	spans, err := Open(SpansPath(dbPath), OpenOptions{CacheMB: o.CacheMB, MmapMB: o.MmapMB, Attach: []Attachment{
		{Schema: CoreSchema, Path: dbPath, CacheMB: o.AttachCacheMB, MmapMB: o.AttachMmapMB},
	}})
	if err != nil {
		main.Close()
		return nil, err
	}
	return &Storage{Main: main, Spans: spans}, nil
}

// Split reports whether the spans family has its own file.
func (s *Storage) Split() bool { return s.Spans != nil }

// Handles returns every writable handle, main first. Each needs its own
// checkpoint loop: a checkpoint only covers the file its handle has as main.
func (s *Storage) Handles() []*DB {
	if s.Spans == nil {
		return []*DB{s.Main}
	}
	return []*DB{s.Main, s.Spans}
}

// Repository returns a repository that reads through Main and routes every
// family's writes to the handle of the file that holds it.
func (s *Storage) Repository() *Repository {
	repo := NewRepository(s.Main.DB)
	if s.Spans != nil {
		repo.SetFamilyWriter(FamilySpans, s.Spans.DB, nil)
	}
	return repo
}

// FinalCheckpoint checkpoints every file. Call after all writers stopped.
func (s *Storage) FinalCheckpoint(log *slog.Logger) {
	for _, h := range s.Handles() {
		h.FinalCheckpoint(log)
	}
}

// Close closes every handle.
func (s *Storage) Close() error {
	var errs []error
	for _, h := range s.Handles() {
		errs = append(errs, h.Close())
	}
	return errors.Join(errs...)
}

// OpenReadDB opens the database at dbPath read-only, attaching the spans file
// when the database uses the split layout.
//
// A reader that starts on a new install before the writer has created the
// spans file still picks the split layout, because main has no spans table.
// Its connections then fail to open until the file exists, and the pool opens
// new ones on the next query, so it recovers without a restart.
func OpenReadDB(ctx context.Context, dbPath string, cacheMB, mmapMB int) (*DB, error) {
	split, err := splitLayoutRO(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	o := OpenOptions{ReadOnly: true, CacheMB: cacheMB, MmapMB: mmapMB}
	if split {
		o.Attach = []Attachment{{Schema: SpansSchema, Path: SpansPath(dbPath), CacheMB: cacheMB, MmapMB: mmapMB}}
	}
	return Open(dbPath, o)
}

func splitLayoutRW(ctx context.Context, main *sql.DB, dbPath string) (bool, error) {
	if dbPath == ":memory:" {
		return false, nil
	}
	if fileExists(SpansPath(dbPath)) {
		return true, nil
	}
	has, err := hasTable(ctx, main, "spans")
	return !has, err
}

func splitLayoutRO(ctx context.Context, dbPath string) (bool, error) {
	if fileExists(SpansPath(dbPath)) || !fileExists(dbPath) {
		return true, nil
	}
	db, err := Open(dbPath, OpenOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer db.Close()
	has, err := hasTable(ctx, db.DB, "spans")
	return !has, err
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasTable reports whether main (the handle's own file) holds table.
func hasTable(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM main.sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("inspect schema: %w", err)
	}
	return n > 0, nil
}

// dropMovedTables drops the spans family's tables from main. They shadow the
// attached spans file (main resolves first), so a split database must not
// have them. Only empty tables are dropped: rows in them mean a single-file
// database that the cut-over has to move first.
func dropMovedTables(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range FamilySpans.Tables() {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM main.sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		var rows int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM main.`+table+` LIMIT 1)`).Scan(&rows); err != nil {
			return err
		}
		if rows > 0 {
			return fmt.Errorf("main database still holds rows in %s; a single-file database needs the spans cut-over before it gets a spans file", table)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE main.`+table); err != nil {
			return fmt.Errorf("drop %s from main: %w", table, err)
		}
	}
	return tx.Commit()
}

// enableIncrementalVacuum turns on auto_vacuum=INCREMENTAL while db holds no
// tables yet. The setting only takes effect on an empty file, and the periodic
// checkpoint's incremental_vacuum returns freed pages to the volume only when
// it is on.
func enableIncrementalVacuum(ctx context.Context, db *sql.DB) error {
	var mode, tables int
	if err := db.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return err
	}
	if mode == 2 {
		return nil
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&tables); err != nil {
		return err
	}
	if tables > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, `PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `VACUUM`)
	return err
}
