package repository

import (
	"fmt"
	"os"
	"sync"
)

// ballastChunk is the write size used to materialise the ballast. Large enough
// to be fast, small enough not to spike memory.
const ballastChunk = 4 << 20 // 4 MiB

// Ballast is reserved disk space that exists only to be surrendered.
//
// It answers the failure that makes a full SpanBarn volume unrecoverable: once
// SQLite reports SQLITE_FULL, retention cannot fix it, because a DELETE is
// itself a write and needs WAL space to commit. The database is then wedged
// until a human rebuilds it by hand — which is exactly what happened in
// production, undetected, for 26 days.
//
// So we pay for a way out in advance. At startup we occupy a few hundred
// megabytes we do not need. When the volume fills, deleting that file is a
// metadata operation that always succeeds even at 100% full, and it hands
// retention enough room to commit the deletes that actually reclaim space.
// Once usage is back under target, the ballast is re-created.
//
// The file must contain real blocks. A sparse file (ftruncate on ext4) reserves
// nothing, so deleting it would free nothing and the whole mechanism would read
// as protection while doing exactly nothing — this codebase's recurring bug
// shape. Ensure therefore writes zeros rather than truncating.
type Ballast struct {
	path string
	size int64

	mu sync.Mutex
}

// NewBallast returns a ballast stored alongside dbPath. A size of zero (or an
// empty dbPath) yields a disabled ballast whose methods are all no-ops, so
// callers need no special-casing.
func NewBallast(dbPath string, size int64) *Ballast {
	if dbPath == "" || dbPath == ":memory:" || size <= 0 {
		return &Ballast{}
	}
	return &Ballast{path: dbPath + ".ballast", size: size}
}

// Enabled reports whether this ballast reserves anything.
func (b *Ballast) Enabled() bool { return b != nil && b.path != "" && b.size > 0 }

// Path is the ballast file's location (empty when disabled).
func (b *Ballast) Path() string {
	if b == nil {
		return ""
	}
	return b.path
}

// Size is the number of bytes the ballast reserves when present.
func (b *Ballast) Size() int64 {
	if b == nil {
		return 0
	}
	return b.size
}

// Present reports whether the ballast file currently exists at full size.
func (b *Ballast) Present() bool {
	if !b.Enabled() {
		return false
	}
	info, err := os.Stat(b.path)
	return err == nil && info.Size() >= b.size
}

// Ensure creates the ballast, or restores it to full size if it is missing or
// short. It is safe to call on every retention cycle: an intact ballast is a
// cheap stat and no writes.
//
// Failure is not fatal to the caller — a volume too full to hold the ballast is
// precisely the situation where we want retention to keep running — so callers
// should log and continue rather than abort.
func (b *Ballast) Ensure() error {
	if !b.Enabled() {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if info, err := os.Stat(b.path); err == nil && info.Size() >= b.size {
		return nil
	}

	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create ballast: %w", err)
	}
	defer f.Close()

	// Write real bytes. Truncate would make a sparse file that reserves nothing.
	zeros := make([]byte, ballastChunk)
	for written := int64(0); written < b.size; {
		n := int64(len(zeros))
		if remaining := b.size - written; remaining < n {
			n = remaining
		}
		if _, err := f.Write(zeros[:n]); err != nil {
			// Partial ballast is worse than none: it occupies space without
			// guaranteeing the reserve. Drop it and report.
			f.Close()
			_ = os.Remove(b.path)
			return fmt.Errorf("write ballast: %w", err)
		}
		written += n
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync ballast: %w", err)
	}
	return nil
}

// Release deletes the ballast, returning the bytes handed back to the
// filesystem. Releasing an absent ballast returns 0 and no error, so the
// emergency path can call it unconditionally.
func (b *Ballast) Release() (int64, error) {
	if !b.Enabled() {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	info, err := os.Stat(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if err := os.Remove(b.path); err != nil {
		return 0, fmt.Errorf("release ballast: %w", err)
	}
	return info.Size(), nil
}
