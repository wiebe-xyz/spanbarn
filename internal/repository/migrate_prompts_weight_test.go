package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

// A prompts shard created before the column gets it on the next startup, and
// its rows read as one call each.
func TestMigrateShardAddsPromptSampleWeight(t *testing.T) {
	ctx := context.Background()
	h, err := NewDB(filepath.Join(t.TempDir(), "prompts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	p, err := goose.NewProvider(goose.DialectSQLite3, h.DB, nil,
		goose.WithDisableGlobalRegistry(true),
		goose.WithTableName(shardVersionTable),
		goose.WithGoMigrations(shardMigrations(FamilyPrompts)...),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO prompt_records
		(project_id, trace_id, span_id, service, name, start_time_us) VALUES (1, 't', 's', 'api', 'chat', 1)`); err != nil {
		t.Fatal(err)
	}

	if err := MigrateShard(ctx, h.DB, FamilyPrompts); err != nil {
		t.Fatal(err)
	}

	var weight sql.NullInt64
	if err := h.DB.QueryRowContext(ctx, `SELECT sample_weight FROM prompt_records`).Scan(&weight); err != nil {
		t.Fatal(err)
	}
	if weight.Valid {
		t.Fatalf("existing row has weight %d, want NULL", weight.Int64)
	}
}

func TestMigrateShardSkipsFamiliesWithoutPrompts(t *testing.T) {
	ctx := context.Background()
	h, err := NewDB(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := MigrateShard(ctx, h.DB, FamilyLogs); err != nil {
		t.Fatal(err)
	}
	if has, err := hasTable(ctx, h.DB, "prompt_records"); err != nil || has {
		t.Fatalf("logs shard has prompt_records=%v, err %v", has, err)
	}
}

func TestAddPromptSampleWeightRunsTwice(t *testing.T) {
	ctx := context.Background()
	h, err := NewDB(filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := MigrateSingleFile(ctx, h.DB); err != nil {
		t.Fatal(err)
	}
	if err := addPromptSampleWeight(ctx, h.DB); err != nil {
		t.Fatalf("second run on a table that has the column: %v", err)
	}
}
