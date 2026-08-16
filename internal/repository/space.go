package repository

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Space describes how much room the database actually has left to grow.
//
// This exists because SpanBarn's production outages have all had the same
// shape: retention is purely time-based, so when ingest volume rises the
// configured window stops fitting on the volume, SQLite hits SQLITE_FULL, and
// every write — telemetry *and* control-plane — fails at once. Nothing in the
// process could previously answer "how full am I?", so nothing could react
// before the wall was hit. Space is that answer.
//
// The subtlety worth spelling out: bytes free *on the volume* is not the same
// as space available *to SQLite*. Pages on the freelist already belong to the
// database file — they count as used against the filesystem, but SQLite will
// happily write into them without growing the file. A database that is 5 GB of
// which 4 GB is freelist has plenty of room despite a 100%-full volume. Treat
// those as available or the guard fires permanently on a healthy database.
type Space struct {
	PageSize      int64 // bytes per SQLite page
	PageCount     int64 // total pages in the database file
	FreelistCount int64 // pages on the freelist, reusable without growing the file
	AutoVacuum    int64 // 0=NONE, 1=FULL, 2=INCREMENTAL

	FileBytes int64 // size of the main database file
	WALBytes  int64 // size of the -wal sidecar

	VolumeBytes     int64 // total size of the filesystem holding the database
	VolumeFreeBytes int64 // bytes available to an unprivileged writer
}

// LiveBytes is the space actually occupied by data, excluding reusable pages.
func (s Space) LiveBytes() int64 { return (s.PageCount - s.FreelistCount) * s.PageSize }

// ReusableBytes is freelist space SQLite can write into without extending the file.
func (s Space) ReusableBytes() int64 { return s.FreelistCount * s.PageSize }

// AvailableBytes is the room left for new data: unused volume space plus the
// freelist pages already reserved inside the database file.
func (s Space) AvailableBytes() int64 { return s.VolumeFreeBytes + s.ReusableBytes() }

// UsedFraction reports how full the volume is on a 0..1 scale, counting
// reclaimable freelist pages as free. It returns 0 when the volume size is
// unknown (an in-memory database, or a statfs that failed), so an unmeasurable
// database is treated as unpressured rather than permanently critical.
func (s Space) UsedFraction() float64 {
	if s.VolumeBytes <= 0 {
		return 0
	}
	f := 1 - float64(s.AvailableBytes())/float64(s.VolumeBytes)
	return math.Min(1, math.Max(0, f))
}

// Measured reports whether the volume statistics were actually obtained. Callers
// that gate behaviour on pressure must not act on an unmeasured Space.
func (s Space) Measured() bool { return s.VolumeBytes > 0 && s.PageSize > 0 }

// DBSpace samples the database's space usage. It is cheap — three pragmas that
// read the header plus one statfs — and safe to call on every retention cycle.
//
// dbPath may be ":memory:" or empty, in which case the filesystem figures are
// left at zero and Measured() reports false.
func (r *Repository) DBSpace(ctx context.Context, dbPath string) (Space, error) {
	var s Space

	for _, p := range []struct {
		pragma string
		dest   *int64
	}{
		{"page_size", &s.PageSize},
		{"page_count", &s.PageCount},
		{"freelist_count", &s.FreelistCount},
		{"auto_vacuum", &s.AutoVacuum},
	} {
		if err := r.db.QueryRowContext(ctx, "PRAGMA "+p.pragma).Scan(p.dest); err != nil {
			return s, err
		}
	}

	if dbPath == "" || dbPath == ":memory:" {
		return s, nil
	}

	if info, err := os.Stat(dbPath); err == nil {
		s.FileBytes = info.Size()
	}
	if info, err := os.Stat(dbPath + "-wal"); err == nil {
		s.WALBytes = info.Size()
	}

	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(dbPath), &st); err != nil {
		// A failed statfs must not fail the caller: retention still has useful
		// work to do without a pressure reading. Measured() stays false.
		return s, nil //nolint:nilerr // deliberate: degrade to unmeasured
	}
	bs := int64(st.Bsize)
	s.VolumeBytes = int64(st.Blocks) * bs
	s.VolumeFreeBytes = int64(st.Bavail) * bs

	return s, nil
}

// IsDiskFull reports whether err is SQLite's SQLITE_FULL or the underlying
// ENOSPC. Worth classifying separately from an ordinary write error: a full
// disk is not transient, so the write worker's retry-then-dead-letter loop just
// burns the batch. Callers use this to shed rather than retry.
func IsDiskFull(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOSPC) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database or disk is full") ||
		strings.Contains(msg, "sqlite_full") ||
		strings.Contains(msg, "no space left on device")
}
