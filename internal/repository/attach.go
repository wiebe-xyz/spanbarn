package repository

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net/url"
	"os"
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
	// UnlessMainHas skips the attachment on a connection whose main file
	// holds this table: the single-file layout, where a spans file next to it
	// is a leftover the writer replaces. Without the file and without the
	// table the ATTACH fails, the connection is not pooled, and the next query
	// tries again.
	UnlessMainHas string
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
//
// Statements run after the attachments, in order: the TEMP views a shard read
// pool puts over its family's files.
type ConnSetup struct {
	Attach     []Attachment
	Statements []string
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
		skip, err := skipAttachment(conn, a)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if err := attachFile(conn, a); err != nil {
			return fmt.Errorf("attach %s as %s: %w", a.Path, a.Schema, err)
		}
		if err := sizeAttachment(conn, a); err != nil {
			return err
		}
	}
	for _, stmt := range setup.Statements {
		if _, err := conn.ExecContext(context.Background(), stmt, nil); err != nil {
			return fmt.Errorf("connection setup: %w", err)
		}
	}
	return nil
}

// attachFile attaches a.Path as a.Schema, read-only. A WAL-mode file with no
// -wal or -shm next to it (a closed shard) cannot be opened mode=ro by a
// reader that cannot create the -shm, which fails the attach. That state has
// no live writer, so the attach retries immutable: SQLite takes no locks and
// needs no sidecar. A file with a sidecar or a hot journal never takes the
// retry, so a shard still being written always attaches mode=ro and sees its
// WAL. An immutable connection does not see a writer that opens the file
// later; pooled connections are recycled after readConnMaxLifetime.
func attachFile(conn sqlite.ExecQuerierContext, a Attachment) error {
	stmt := "ATTACH DATABASE ? AS " + a.Schema
	arg := []driver.NamedValue{{Ordinal: 1, Value: "file:" + a.Path + "?mode=ro"}}
	_, err := conn.ExecContext(context.Background(), stmt, arg)
	if err == nil || hasSidecar(a.Path) {
		return err
	}
	arg[0].Value = "file:" + a.Path + "?immutable=1"
	_, err = conn.ExecContext(context.Background(), stmt, arg)
	return err
}

func hasSidecar(path string) bool {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return true
		}
	}
	return false
}

func skipAttachment(conn sqlite.ExecQuerierContext, a Attachment) (bool, error) {
	if a.UnlessMainHas == "" {
		return false, nil
	}
	arg := []driver.NamedValue{{Ordinal: 1, Value: a.UnlessMainHas}}
	rows, err := conn.QueryContext(context.Background(),
		`SELECT count(*) FROM main.sqlite_master WHERE type = 'table' AND name = ?`, arg)
	if err != nil {
		return false, fmt.Errorf("attach %s: inspect main: %w", a.Path, err)
	}
	defer rows.Close()
	row := make([]driver.Value, 1)
	if err := rows.Next(row); err != nil {
		return false, fmt.Errorf("attach %s: inspect main: %w", a.Path, err)
	}
	n, _ := row[0].(int64)
	return n > 0, nil
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
