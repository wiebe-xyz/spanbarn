package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pressly/goose/v3"
)

// The spans family has its own migration track, recorded in spansVersionTable
// of whichever file holds the family: the spans file in the split layout, main
// in the single-file layout. Main-track migrations must not touch span-family
// tables any more: in the split layout main does not have them. A schema
// change to a span-family table is a new entry in spansMigrations.
const spansVersionTable = "goose_spans_version"

// spansBaselineVersion is the main-track version whose span-family schema the
// spans track starts from.
const spansBaselineVersion = 41

func spansMigrations() []*goose.Migration {
	return []*goose.Migration{
		goose.NewGoMigration(1, &goose.GoFunc{RunTx: spansBaselineUp}, nil),
	}
}

// MigrateSpans runs the spans migration track against db. goose runs the
// track on one pinned connection and writable handles have only one, so every
// spans migration runs inside the transaction goose passes (GoFunc.RunTx).
func MigrateSpans(ctx context.Context, db *sql.DB) error {
	p, err := goose.NewProvider(goose.DialectSQLite3, db, nil,
		goose.WithDisableGlobalRegistry(true),
		goose.WithTableName(spansVersionTable),
		goose.WithGoMigrations(spansMigrations()...),
	)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// spansBaselineUp creates the span-family tables and indexes as main-track
// migrations 1 to spansBaselineVersion leave them. Every statement carries IF
// NOT EXISTS, so on a single-file database, where those migrations already
// created them, it changes nothing.
func spansBaselineUp(ctx context.Context, tx *sql.Tx) error {
	ddl, err := spansBaselineDDL(ctx)
	if err != nil {
		return err
	}
	for _, stmt := range ddl {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("spans baseline: %w", err)
		}
	}
	return nil
}

var createPrefix = regexp.MustCompile(`(?i)^CREATE\s+(UNIQUE\s+)?(TABLE|INDEX)\s+(IF\s+NOT\s+EXISTS\s+)?`)

// spansBaselineDDL migrates a scratch in-memory database to
// spansBaselineVersion and reads back the span-family schema. Reading it from
// the migrated schema keeps one definition of these tables: migrations 1 to 41
// stay the source, and the baseline cannot drift from what they build.
//
// The scratch database is a file in a temporary directory: goose holds one
// connection for the whole run while some migrations open another, and every
// connection to ":memory:" would get a database of its own.
func spansBaselineDDL(ctx context.Context) ([]string, error) {
	dir, err := os.MkdirTemp("", "spanbarn-baseline-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	scratch, err := sql.Open("sqlite", filepath.Join(dir, "scratch.db"))
	if err != nil {
		return nil, err
	}
	defer scratch.Close()

	p, err := goose.NewProvider(goose.DialectSQLite3, scratch, nil)
	if err != nil {
		return nil, err
	}
	if _, err := p.UpTo(ctx, spansBaselineVersion); err != nil {
		return nil, fmt.Errorf("migrate scratch database: %w", err)
	}

	tables := FamilySpans.Tables()
	args := make([]any, len(tables))
	for i, t := range tables {
		args[i] = t
	}
	rows, err := scratch.QueryContext(ctx, `SELECT sql FROM sqlite_master
		WHERE tbl_name IN (`+placeholderList(len(tables))+`) AND sql IS NOT NULL
		ORDER BY type = 'index', rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ddl []string
	for rows.Next() {
		var stmt string
		if err := rows.Scan(&stmt); err != nil {
			return nil, err
		}
		ddl = append(ddl, createPrefix.ReplaceAllStringFunc(stmt, ifNotExists))
	}
	return ddl, rows.Err()
}

func ifNotExists(prefix string) string {
	m := createPrefix.FindStringSubmatch(prefix)
	out := "CREATE "
	if m[1] != "" {
		out += "UNIQUE "
	}
	return out + strings.ToUpper(m[2]) + " IF NOT EXISTS "
}
