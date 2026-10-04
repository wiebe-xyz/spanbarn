package repository

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"modernc.org/sqlite"
)

// Attachment is a database file that every connection of a handle attaches
// read-only under Schema.
type Attachment struct {
	Schema string
	Path   string
	// CacheMB and MmapMB size the attached file's page cache and mmap window.
	// The handle's own cache_size and mmap_size pragmas apply to main only.
	// Zero leaves SQLite's defaults.
	CacheMB int
	MmapMB  int
}

// ConnSetup is what a handle runs on every new pooled connection: it attaches
// the listed files read-only, in order. ATTACH belongs to a connection, so a
// pool with more than one connection needs it on each of them; running it once
// through Exec reaches whichever connection happened to serve that call.
//
// A read-only attachment also enforces the one-family-per-write rule: a write
// that reaches a table in another family's file fails with "attempt to write a
// readonly database" instead of taking that file's write lock behind its own
// writer's back.
type ConnSetup struct {
	Attach []Attachment
}

// handleParam carries a handle's registry key in its DSN. SQLite ignores URI
// parameters it does not know, so the key reaches the connection hook and
// nothing else.
const handleParam = "_spanbarn_handle"

var (
	connSetups sync.Map // handle key -> *ConnSetup
	handleSeq  atomic.Uint64
)

func init() {
	sqlite.RegisterConnectionHook(runConnSetup)
}

// registerConnSetup stores setup under a fresh key and returns the key.
func registerConnSetup(setup *ConnSetup) string {
	key := fmt.Sprintf("h%d", handleSeq.Add(1))
	connSetups.Store(key, setup)
	return key
}

func runConnSetup(conn sqlite.ExecQuerierContext, dsn string) error {
	key := handleKeyFromDSN(dsn)
	if key == "" {
		return nil
	}
	v, ok := connSetups.Load(key)
	if !ok {
		return nil
	}
	setup := v.(*ConnSetup)
	for _, a := range setup.Attach {
		if !validSchemaName(a.Schema) {
			return fmt.Errorf("attach %s: invalid schema name %q", a.Path, a.Schema)
		}
		arg := []driver.NamedValue{{Ordinal: 1, Value: "file:" + a.Path + "?mode=ro"}}
		if _, err := conn.ExecContext(context.Background(), "ATTACH DATABASE ? AS "+a.Schema, arg); err != nil {
			return fmt.Errorf("attach %s as %s: %w", a.Path, a.Schema, err)
		}
		if err := sizeAttachment(conn, a); err != nil {
			return err
		}
	}
	return nil
}

func sizeAttachment(conn sqlite.ExecQuerierContext, a Attachment) error {
	var pragmas []string
	if a.CacheMB > 0 {
		pragmas = append(pragmas, fmt.Sprintf("PRAGMA %s.cache_size = -%d", a.Schema, a.CacheMB*1024))
	}
	if a.MmapMB > 0 {
		pragmas = append(pragmas, fmt.Sprintf("PRAGMA %s.mmap_size = %d", a.Schema, int64(a.MmapMB)*1024*1024))
	}
	for _, p := range pragmas {
		if _, err := conn.ExecContext(context.Background(), p, nil); err != nil {
			return fmt.Errorf("size attachment %s: %w", a.Schema, err)
		}
	}
	return nil
}

func handleKeyFromDSN(dsn string) string {
	i := strings.IndexByte(dsn, '?')
	if i < 0 {
		return ""
	}
	q, err := url.ParseQuery(dsn[i+1:])
	if err != nil {
		return ""
	}
	return q.Get(handleParam)
}

// validSchemaName accepts the identifiers this package generates for attached
// schemas (letters, digits, underscore, not starting with a digit), so a schema
// name can be spliced into ATTACH without quoting.
func validSchemaName(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for _, c := range s {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}
