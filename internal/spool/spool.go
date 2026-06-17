package spool

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	// DefaultMaxBytes is the default spool file size before rotation (64 MiB).
	DefaultMaxBytes = 64 << 20
	spoolFileName   = "spool.ndjson"
	oldFileName     = "spool.ndjson.old"
	cursorFileName  = "cursor"
)

// Spool is a durable, append-only NDJSON file for span records.
// It supports rotation when the file exceeds maxBytes.
type Spool struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64
	file     *os.File
	size     int64
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

// Read reads records from the spool file starting at byte offset cursor.
// It returns up to limit records and the next cursor position.
// After rotation the cursor may exceed the new (smaller) file — reset to 0.
func (s *Spool) Read(cursor int64, limit int) ([]model.SpanRecord, int64, error) {
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

	var records []model.SpanRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	pos := cursor

	for scanner.Scan() && len(records) < limit {
		line := scanner.Bytes()
		var rec model.SpanRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			pos += int64(len(line)) + 1
			continue
		}
		records = append(records, rec)
		pos += int64(len(line)) + 1
	}

	if err := scanner.Err(); err != nil {
		return records, pos, fmt.Errorf("spool scan: %w", err)
	}

	return records, pos, nil
}

// SaveCursor persists the cursor position to disk.
func (s *Spool) SaveCursor(pos int64) error {
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
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

// rotate renames the current spool file to .old and opens a fresh one.
// Must be called with s.mu held.
func (s *Spool) rotate() error {
	if err := s.file.Close(); err != nil {
		return err
	}

	cur := filepath.Join(s.dir, spoolFileName)
	old := filepath.Join(s.dir, oldFileName)

	// Remove previous .old if it exists, then rename current.
	_ = os.Remove(old)
	if err := os.Rename(cur, old); err != nil {
		return err
	}

	f, err := os.OpenFile(cur, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}

	s.file = f
	s.size = 0
	return nil
}
