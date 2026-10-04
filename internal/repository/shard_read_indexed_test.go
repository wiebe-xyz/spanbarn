package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoIndexedByOnShardedFamilies guards the read views: a sharded family's
// tables are TEMP views over the shards, and SQLite rejects INDEXED BY on a
// view, so no repository query may name one of their indexes.
func TestNoIndexedByOnShardedFamilies(t *testing.T) {
	store := openTestStorage(t, filepath.Join(t.TempDir(), "spanbarn.db"))
	var indexes []string
	for _, sf := range shardedFamilies {
		for _, table := range sf.family.Tables() {
			rows, err := store.Main.QueryContext(context.Background(),
				`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ?`, table)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatal(err)
				}
				indexes = append(indexes, name)
			}
			rows.Close()
		}
	}
	if len(indexes) == 0 {
		t.Fatal("no indexes found on the sharded families")
	}
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(string(b)), " ")
		for _, idx := range indexes {
			if strings.Contains(text, "INDEXED BY "+idx) {
				t.Errorf("%s uses INDEXED BY %s on a sharded family", src, idx)
			}
		}
	}
}
