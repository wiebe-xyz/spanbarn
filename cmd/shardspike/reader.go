package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// openReader returns a read-only connection that sees the layout's tables
// under their usual unqualified names, so repository methods run unchanged:
//
//   - L1 opens the single file.
//   - L2 opens the config file and attaches spans.db and heavy.db. SQLite
//     resolves an unqualified table name in temp, then main, then each attached
//     database in order, so "spans" finds spans.db with no view in between.
//   - L3 opens the config file, attaches the daily files and creates TEMP views
//     named after each table: SELECT * FROM d0.t UNION ALL SELECT * FROM d1.t ...
//
// The pool is capped at one connection, because ATTACH and TEMP views belong
// to a connection and a second pooled connection would see neither.
func openReader(ctx context.Context, root, layout string) (*sql.DB, error) {
	dir := layoutDir(root, layout)
	if layout == layoutSingle {
		db, err := repository.NewReadOnlyDB(filepath.Join(dir, singleFile))
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		return db.DB, nil
	}
	db, err := repository.NewReadOnlyDB(filepath.Join(dir, configFile))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	files, err := dataFiles(dir)
	if err == nil {
		err = attachAll(ctx, db.DB, files)
	}
	if err == nil && layout == layoutShards {
		err = createUnionViews(ctx, db.DB, len(files))
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return db.DB, nil
}

// dataFiles lists the layout's data files (everything but the config file),
// sorted so the daily shards attach oldest first.
func dataFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") && e.Name() != configFile {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// attachAlias is the schema name of the i-th attached data file.
func attachAlias(i int) string { return fmt.Sprintf("d%d", i) }

func attachAll(ctx context.Context, db *sql.DB, files []string) error {
	for i, f := range files {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("ATTACH DATABASE ? AS %s", attachAlias(i)), f); err != nil {
			return fmt.Errorf("attach %s: %w", f, err)
		}
	}
	return nil
}

// createUnionViews creates one TEMP view per in-scope table over n attached
// shards.
func createUnionViews(ctx context.Context, db *sql.DB, n int) error {
	for _, t := range inScope {
		parts := make([]string, n)
		for i := range n {
			parts[i] = fmt.Sprintf("SELECT * FROM %s.%s", attachAlias(i), t)
		}
		q := fmt.Sprintf("CREATE TEMP VIEW %s AS %s", t, strings.Join(parts, " UNION ALL "))
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("view %s: %w", t, err)
		}
	}
	return nil
}
