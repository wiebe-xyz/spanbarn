package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// The three layouts the spike compares. Every layout holds the same rows; the
// L2 and L3 files are filled by copying from the L1 file.
const (
	layoutSingle = "single" // L1: today's single file
	layoutSplit  = "split"  // L2: spans file + heavy-tables file + config file
	layoutShards = "shards" // L3: one file per UTC day + config file
)

var layouts = []string{layoutSingle, layoutSplit, layoutShards}

// inScope are the high-volume tables issue #235 moves out of the main file.
var inScope = []string{"spans", "trace_summaries", "logs", "metrics", "error_samples", "prompt_records"}

// splitFiles maps each L2 data file to the tables it owns.
var splitFiles = map[string][]string{
	"spans.db": {"spans", "trace_summaries"},
	"heavy.db": {"logs", "metrics", "error_samples", "prompt_records"},
}

// timeColumn is the column a table is sharded on.
var timeColumn = map[string]string{
	"spans": "ingested_at", "trace_summaries": "ingested_at", "logs": "ingested_at",
	"metrics": "ingested_at", "prompt_records": "ingested_at", "error_samples": "sampled_at",
}

const (
	singleFile = "single.db"
	configFile = "config.db"
)

func layoutDir(root, layout string) string { return filepath.Join(root, layout) }

func shardName(day time.Time) string { return "day-" + day.Format("20060102") + ".db" }

// createFile makes a migrated database at path and drops every table in drop,
// so the file holds the current schema minus the tables another file owns.
func createFile(ctx context.Context, path string, drop []string) error {
	db, err := repository.NewDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := repository.Migrate(db.DB); err != nil {
		return fmt.Errorf("migrate %s: %w", path, err)
	}
	for _, t := range drop {
		if _, err := db.ExecContext(ctx, "DROP TABLE "+t); err != nil {
			return fmt.Errorf("drop %s in %s: %w", t, path, err)
		}
	}
	return nil
}

func without(all, keep []string) []string {
	var out []string
	for _, t := range all {
		if !slices.Contains(keep, t) {
			out = append(out, t)
		}
	}
	return out
}

// buildSingle creates the L1 database and fills it with the generator.
func buildSingle(ctx context.Context, root string, g *generator) error {
	dir := layoutDir(root, layoutSingle)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, singleFile)
	if err := createFile(ctx, path, nil); err != nil {
		return err
	}
	db, err := repository.NewDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := g.generate(ctx, db.DB); err != nil {
		return err
	}
	return checkpoint(ctx, db.DB)
}

// copyPlan is one target file and the rows it takes from the L1 file.
type copyPlan struct {
	file   string
	tables []string
	from   time.Time // zero: every row
	to     time.Time
}

func splitPlans() []copyPlan {
	return []copyPlan{
		{file: "spans.db", tables: splitFiles["spans.db"]},
		{file: "heavy.db", tables: splitFiles["heavy.db"]},
	}
}

func shardPlans(w window) []copyPlan {
	var out []copyPlan
	for _, d := range w.days() {
		out = append(out, copyPlan{file: shardName(d), tables: inScope, from: d, to: d.Add(24 * time.Hour)})
	}
	// Spans ingested a few seconds after the window end belong to the last day.
	out[len(out)-1].to = w.End.Add(time.Hour)
	return out
}

// buildFromSingle writes the L2 or L3 layout by copying rows out of the L1
// file, so all three layouts hold exactly the same data.
func buildFromSingle(ctx context.Context, root, layout string, plans []copyPlan) error {
	dir := layoutDir(root, layout)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	src := filepath.Join(layoutDir(root, layoutSingle), singleFile)
	if err := copyConfig(ctx, src, filepath.Join(dir, configFile)); err != nil {
		return err
	}
	tables, err := tableNames(ctx, src)
	if err != nil {
		return err
	}
	for _, p := range plans {
		path := filepath.Join(dir, p.file)
		if err := createFile(ctx, path, without(tables, p.tables)); err != nil {
			return err
		}
		if err := copyRows(ctx, src, path, p); err != nil {
			return err
		}
	}
	return nil
}

// copyConfig creates the config file (every table except the in-scope ones)
// and copies the project rows into it.
func copyConfig(ctx context.Context, src, dst string) error {
	if err := createFile(ctx, dst, inScope); err != nil {
		return err
	}
	return withAttached(ctx, dst, src, func(db *sql.DB) error {
		_, err := db.ExecContext(ctx, "INSERT INTO projects SELECT * FROM src.projects")
		return err
	})
}

func copyRows(ctx context.Context, src, dst string, p copyPlan) error {
	return withAttached(ctx, dst, src, func(db *sql.DB) error {
		for _, t := range p.tables {
			cols, err := storedColumns(ctx, db, t)
			if err != nil {
				return err
			}
			q, args := copyQuery(t, cols, p)
			if _, err := db.ExecContext(ctx, q, args...); err != nil {
				return fmt.Errorf("copy %s into %s: %w", t, dst, err)
			}
		}
		return checkpoint(ctx, db)
	})
}

// copyQuery copies a table's rows, keeping their ids, so a row has the same
// primary key in every layout. Generated columns are skipped by naming the
// stored columns explicitly.
func copyQuery(table string, columns []string, p copyPlan) (string, []any) {
	cols := strings.Join(columns, ", ")
	q := fmt.Sprintf("INSERT INTO main.%s (%s) SELECT %s FROM src.%s", table, cols, cols, table)
	if p.from.IsZero() {
		return q, nil
	}
	col := timeColumn[table]
	return q + fmt.Sprintf(" WHERE %s >= ? AND %s < ?", col, col), []any{sqlTime(p.from), sqlTime(p.to)}
}

// withAttached opens dst as the writable main database with src attached as
// "src" on a single connection, so the ATTACH holds for every statement fn runs.
func withAttached(ctx context.Context, dst, src string, fn func(*sql.DB) error) error {
	db, err := repository.NewDB(dst)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "ATTACH DATABASE ? AS src", src); err != nil {
		return err
	}
	return fn(db.DB)
}

func checkpoint(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// tableNames lists the user tables in the database at path.
func tableNames(ctx context.Context, path string) ([]string, error) {
	db, err := repository.NewReadOnlyDB(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return queryStrings(ctx, db.DB,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
}

// storedColumns lists a table's columns minus generated ones (table_xinfo
// marks those hidden 2 or 3), which an INSERT cannot name.
func storedColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	return queryStrings(ctx, db, "SELECT name FROM pragma_table_xinfo(?) WHERE hidden = 0 ORDER BY cid", table)
}

func queryStrings(ctx context.Context, db *sql.DB, q string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
