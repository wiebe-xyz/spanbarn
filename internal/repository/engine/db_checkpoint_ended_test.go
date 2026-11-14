package engine

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// A checkpoint that fails with an error inside its budget names the cause
// error, which tells it apart from a timeout.
func TestEndedCheckpointCauses(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	open := func(t *testing.T) *DB {
		t.Helper()
		d, err := Open(filepath.Join(t.TempDir(), "shard.db"), OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec("CREATE TABLE t (x); INSERT INTO t VALUES (1)"); err != nil {
			t.Fatal(err)
		}
		return d
	}

	d := open(t)
	if r := d.CheckpointEnded(time.Second, log); r.Cause != "" || r.Frames != 0 {
		t.Errorf("free checkpoint: cause %q, frames %d", r.Cause, r.Frames)
	}
	d.Close()
	if r := d.CheckpointEnded(time.Second, log); r.Cause != "error" {
		t.Errorf("checkpoint on a closed handle: cause %q, want error", r.Cause)
	}

	d = open(t)
	defer d.Close()
	held, err := d.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if r := d.CheckpointEnded(50*time.Millisecond, log); r.Cause != "timeout" {
		t.Errorf("checkpoint behind a held connection: cause %q, want timeout", r.Cause)
	}
}
