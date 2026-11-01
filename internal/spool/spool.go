package spool

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	// DefaultMaxBytes is the default spool file size before rotation (64 MiB).
	DefaultMaxBytes = 64 << 20
	spoolFileName   = "spool.ndjson"
	oldFileName     = "spool.ndjson.old"
	cursorFileName  = "cursor"

	// undecodableLogInterval caps the undecodable-line ERROR at one line per
	// pod per minute, however damaged the file is.
	undecodableLogInterval = time.Minute
)

// Spool is a durable, append-only NDJSON file for span records.
// It supports rotation when the file exceeds maxBytes.
type Spool struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64
	file     *os.File
	size     int64

	// readUndecodable is what the last Read skipped. It counts once the reader
	// moves past those lines, because a failed publish reads them again.
	readUndecodable int64
	undecodable     int64
	unloggedLines   int64
	lastLinesLog    time.Time
}

// NewSpool creates or opens a spool in the given directory.
func NewSpool(dir string, maxBytes int64) (*Spool, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("spool mkdir %s: %w", dir, err)
	}

	path := filepath.Join(dir, spoolFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("spool open %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("spool stat %s: %w", path, err)
	}

	return &Spool{
		dir:      dir,
		maxBytes: maxBytes,
		file:     f,
		size:     info.Size(),
	}, nil
}

// Size returns the current spool file size in bytes.
func (s *Spool) Size() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Write appends span records as NDJSON lines. It rotates the file if needed.
func (s *Spool) Write(records []model.SpanRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, rec := range records {
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("spool marshal: %w", err)
		}
		line := append(data, '\n')

		n, err := s.file.Write(line)
		if err != nil {
			return fmt.Errorf("spool write: %w", err)
		}
		s.size += int64(n)
	}

	// Rotate after writing the batch if we exceeded the limit.
	if s.size >= s.maxBytes {
		if err := s.rotate(); err != nil {
			return fmt.Errorf("spool rotate: %w", err)
		}
	}

	return nil
}

// Read reads records starting at byte offset cursor and returns up to limit of
// them with the next cursor position. A rotated file is finished first, because
// the cursor still points into it, and removed once it has nothing left: its
// presence is what says it holds unread records, also across a restart. The lock
// keeps a rotation from moving the files between those steps.
func (s *Spool) Read(cursor int64, limit int) ([]model.SpanRecord, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.readUndecodable = 0
	old := filepath.Join(s.dir, oldFileName)
	if _, err := os.Stat(old); err == nil {
		records, next, err := s.readFile(old, cursor, limit)
		if err != nil || len(records) > 0 {
			return records, next, err
		}
		if err := os.Remove(old); err != nil {
			return nil, cursor, fmt.Errorf("spool remove old: %w", err)
		}
		cursor = 0
		s.countUndecodable(time.Now())
	}

	path := filepath.Join(s.dir, spoolFileName)

	if cursor > 0 {
		if info, err := os.Stat(path); err == nil && cursor > info.Size() {
			cursor = 0
		}
	}

	return s.readFile(path, cursor, limit)
}

func (s *Spool) readFile(path string, cursor int64, limit int) ([]model.SpanRecord, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cursor, nil
		}
		return nil, cursor, fmt.Errorf("spool read open: %w", err)
	}
	defer f.Close()

	if cursor > 0 {
		if _, err := f.Seek(cursor, io.SeekStart); err != nil {
			return nil, cursor, fmt.Errorf("spool seek: %w", err)
		}
	}

	// A Reader rather than a Scanner: a Scanner caps the line at its buffer size,
	// and one span larger than that stalls replay at the same offset forever. A
	// line here is bounded by the ingest body limit instead.
	var records []model.SpanRecord
	reader := bufio.NewReaderSize(f, 64<<10)
	pos := cursor

	for len(records) < limit {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			// A trailing fragment with no newline is a write still in flight.
			// Leave it unread so the cursor never skips bytes the writer has not
			// finished.
			break
		}
		if err != nil {
			return records, pos, fmt.Errorf("spool read: %w", err)
		}
		pos += int64(len(line))

		var rec model.SpanRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			s.readUndecodable++
			continue
		}
		records = append(records, rec)
	}

	return records, pos, nil
}

// Undecodable returns how many lines this process skipped because they did not
// decode. A line like that names no project, so the ledger can only show it as a
// gap, and this count is what ties the gap to the spool.
func (s *Spool) Undecodable() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.undecodable
}

// countUndecodable moves the last Read's skipped lines into the total. Must be
// called with s.mu held.
func (s *Spool) countUndecodable(now time.Time) {
	s.undecodable += s.readUndecodable
	s.unloggedLines += s.readUndecodable
	s.readUndecodable = 0
	if s.unloggedLines > 0 && now.Sub(s.lastLinesLog) >= undecodableLogInterval {
		s.logUndecodable(now)
	}
}

// logUndecodable must be called with s.mu held.
func (s *Spool) logUndecodable(now time.Time) {
	slog.Default().Error("spool: skipped undecodable lines", "lines", s.unloggedLines, "total", s.undecodable)
	s.unloggedLines = 0
	s.lastLinesLog = now
}

// SaveCursor persists the cursor position to disk.
func (s *Spool) SaveCursor(pos int64) error {
	s.mu.Lock()
	s.countUndecodable(time.Now())
	s.mu.Unlock()

	path := filepath.Join(s.dir, cursorFileName)
	data := []byte(fmt.Sprintf("%d", pos))
	return os.WriteFile(path, data, 0o644)
}

// LoadCursor reads the persisted cursor position. Returns 0 if no cursor file exists.
func (s *Spool) LoadCursor() (int64, error) {
	path := filepath.Join(s.dir, cursorFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("spool load cursor: %w", err)
	}
	var pos int64
	if _, err := fmt.Sscanf(string(data), "%d", &pos); err != nil {
		return 0, fmt.Errorf("spool parse cursor: %w", err)
	}
	return pos, nil
}

// Close flushes and closes the spool file.
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unloggedLines > 0 {
		s.logUndecodable(time.Now())
	}
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

// rotate renames the current spool file to .old and opens a fresh one. An .old
// that is still there has unread records and the reader's cursor points into it,
// so it stays and the current file is emptied instead: the spool never holds more
// than two files, and a reader a whole file behind loses the newer one.
// Must be called with s.mu held.
func (s *Spool) rotate() error {
	if err := s.file.Close(); err != nil {
		return err
	}

	cur := filepath.Join(s.dir, spoolFileName)
	old := filepath.Join(s.dir, oldFileName)

	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if _, err := os.Stat(old); err == nil {
		slog.Default().Error("spool: reader is a whole file behind, dropping the newest records", "bytes", s.size)
		flags |= os.O_TRUNC
	} else if err := os.Rename(cur, old); err != nil {
		return err
	}

	f, err := os.OpenFile(cur, flags, 0o644)
	if err != nil {
		return err
	}

	s.file = f
	s.size = 0
	return nil
}
