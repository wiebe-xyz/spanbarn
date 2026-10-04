package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/pressly/goose/v3"
)

// Every shard file has its own migration track, recorded in shardVersionTable
// of that file. The track is per family: a logs shard runs the logs
// migrations. A new shard is created at the current version of its track; on
// startup the writer migrates every shard listed in the shards table. A schema
// change to a sharded table is a new entry in shardMigrations.
const shardVersionTable = "goose_shard_version"

func shardMigrations(f Family) []*goose.Migration {
	return []*goose.Migration{
		goose.NewGoMigration(1, &goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			return shardBaselineUp(ctx, tx, f)
		}}, nil),
	}
}

// MigrateShard runs f's shard migration track against db, a handle on one
// shard file.
func MigrateShard(ctx context.Context, db *sql.DB, f Family) error {
	p, err := goose.NewProvider(goose.DialectSQLite3, db, nil,
		goose.WithDisableGlobalRegistry(true),
		goose.WithTableName(shardVersionTable),
		goose.WithGoMigrations(shardMigrations(f)...),
	)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// shardBaselineUp creates f's tables and indexes as main-track migrations 1 to
// spansBaselineVersion leave them.
func shardBaselineUp(ctx context.Context, tx *sql.Tx, f Family) error {
	ddl, err := cachedBaselineDDL(ctx, f)
	if err != nil {
		return err
	}
	for _, stmt := range ddl {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s shard baseline: %w", f, err)
		}
	}
	return nil
}

var baselineCache struct {
	sync.Mutex
	ddl map[Family][]string
}

// cachedBaselineDDL is familyBaselineDDL for f's tables, built once per
// process: a writer creates a shard per family every period, and the scratch
// migration costs more than the shard does.
func cachedBaselineDDL(ctx context.Context, f Family) ([]string, error) {
	baselineCache.Lock()
	defer baselineCache.Unlock()
	if ddl, ok := baselineCache.ddl[f]; ok {
		return ddl, nil
	}
	ddl, err := familyBaselineDDL(ctx, f.Tables())
	if err != nil {
		return nil, err
	}
	if baselineCache.ddl == nil {
		baselineCache.ddl = map[Family][]string{}
	}
	baselineCache.ddl[f] = ddl
	return ddl, nil
}
