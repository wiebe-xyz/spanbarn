package repository

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// tableColumns maps a schema-qualified table to its column names.
type tableColumns map[string][]string

// sources lists the tables the view of table unions, newest first. The logs
// view also reads main's kept_logs when it exists.
func (p *familyPool) sources(table string) []string {
	out := make([]string, 0, len(p.segments)+1)
	for _, schema := range p.segments {
		out = append(out, schema+"."+table)
	}
	if p.kept && table == "logs" {
		out = append(out, "main.kept_logs")
	}
	return out
}

// readColumns reads the columns of every table f's views union, on db, a handle
// with the pool's files attached.
func (p *familyPool) readColumns(ctx context.Context, db *sql.DB, f Family) (tableColumns, error) {
	out := tableColumns{}
	for _, table := range f.Tables() {
		for _, source := range p.sources(table) {
			schema, name, _ := strings.Cut(source, ".")
			cols, err := columnsOf(ctx, db, schema, name)
			if err != nil {
				return nil, fmt.Errorf("columns of %s: %w", source, err)
			}
			out[source] = cols
		}
	}
	return out, nil
}

// readColumnsOnce fills p.columns through a short-lived handle: the views go in
// the setup of the pool's handle, so they have to be known before it opens.
func (p *familyPool) readColumnsOnce(ctx context.Context, path string, opts OpenOptions, f Family) error {
	db, err := Open(path, opts)
	if err != nil {
		return err
	}
	defer db.Close()
	p.columns, err = p.readColumns(ctx, db.DB, f)
	return err
}

// viewSQL creates the TEMP view of table over its sources. The sources can
// differ: a shard migration adds a column to the shards that exist, and after a
// rollback the older image creates the next shard without it. A plain SELECT *
// union then fails on every read of the family, so each source names its
// columns and reads NULL for one it lacks.
func (p *familyPool) viewSQL(table string) (string, error) {
	sources := p.sources(table)
	var names []string
	for _, source := range sources {
		for _, col := range p.columns[source] {
			if !slices.Contains(names, col) {
				names = append(names, col)
			}
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no columns for %s in %s", table, strings.Join(sources, ", "))
	}
	parts := make([]string, len(sources))
	for i, source := range sources {
		exprs := make([]string, len(names))
		for j, name := range names {
			if slices.Contains(p.columns[source], name) {
				exprs[j] = quoteColumn(name)
			} else {
				exprs[j] = "NULL AS " + quoteColumn(name)
			}
		}
		parts[i] = "SELECT " + strings.Join(exprs, ", ") + " FROM " + source
	}
	return "CREATE TEMP VIEW " + table + " AS " + strings.Join(parts, " UNION ALL "), nil
}

// columnsUnchanged reports whether p's views still match the files. A writer
// starting on a newer image migrates shards the pool already holds, and a view
// built before that reads NULL for the new column until it is rebuilt. A failed
// read counts as unchanged: the next refresh reads again.
func (s *ShardReaders) columnsUnchanged(ctx context.Context, f Family, p *familyPool) bool {
	ctx, cancel := context.WithTimeout(ctx, columnCheckTimeout)
	defer cancel()
	cols, err := p.readColumns(ctx, p.db.DB, f)
	if err != nil {
		s.logger.Info("shard column check failed", "family", f.String(), "error", err)
		return true
	}
	return maps.EqualFunc(cols, p.columns, slices.Equal[[]string])
}

// columnsOf returns the columns of schema.table in table order.
func columnsOf(ctx context.Context, db querier, schema, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?, ?) ORDER BY cid`, table, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	return cols, rows.Err()
}

// querier is the read side of *sql.DB and *sql.Conn.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func quoteColumn(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
